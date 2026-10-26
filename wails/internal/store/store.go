// Package store persists the recitation state in a local SQLite file.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"recitebook/internal/engine"

	"modernc.org/sqlite"
)

// ErrNoState means the database has no saved round yet.
var ErrNoState = errors.New("store: no saved state")

// ErrCorrupt means the database file or its contents cannot be read.
var ErrCorrupt = errors.New("store: database is corrupt")

// SettingAPIKey is the settings key that must never be exported.
const SettingAPIKey = "llm_api_key"

const exportVersion = 1

// Store is the persistence surface used by the app.
type Store interface {
	Load() (engine.State, error)
	Save(engine.State) error
	AppendAttempt(listNo int, a engine.Attempt) error
	Export(io.Writer) error
	Import(io.Reader) error
}

// SQLite is a Store backed by a SQLite database file.
type SQLite struct {
	db *sql.DB
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS rounds (
		round INTEGER PRIMARY KEY,
		seq INTEGER NOT NULL,
		queue TEXT NOT NULL,
		current INTEGER NOT NULL,
		quiz TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS attempts (
		round INTEGER NOT NULL,
		seq INTEGER NOT NULL,
		list_no INTEGER NOT NULL,
		question_id TEXT NOT NULL,
		result TEXT NOT NULL,
		via TEXT NOT NULL,
		score INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (round, seq)
	)`,
	`CREATE TABLE IF NOT EXISTS list_states (
		round INTEGER NOT NULL,
		list_no INTEGER NOT NULL,
		pending TEXT NOT NULL DEFAULT '[]',
		first_wrong TEXT NOT NULL DEFAULT '[]',
		completed INTEGER NOT NULL DEFAULT 0,
		previewed INTEGER NOT NULL DEFAULT 0,
		"group" TEXT NOT NULL DEFAULT '[]',
		group_at INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (round, list_no)
	)`,
	`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS chats (
		question_id TEXT PRIMARY KEY,
		messages TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`,
}

// Open opens or creates a database at path.
func Open(path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, classify(err)
	}
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, classify(err)
	}
	return s, nil
}

func (s *SQLite) migrate() error {
	for _, statement := range schema {
		if _, err := s.db.Exec(statement); err != nil {
			return err
		}
	}
	if err := s.ensureAttemptScore(); err != nil {
		return err
	}
	if err := s.ensureListPreviewed(); err != nil {
		return err
	}
	if err := s.ensureListGroup(); err != nil {
		return err
	}
	return s.healZeroAttemptCompleted()
}

// ensureAttemptScore adds the score column to databases created before grades
// were stored. Old attempts keep the 0 default.
func (s *SQLite) ensureAttemptScore() error {
	found, err := s.columnExists("attempts", "score")
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE attempts ADD COLUMN score INTEGER NOT NULL DEFAULT 0`)
	return err
}

// ensureListPreviewed adds the previewed column to databases created before
// the card preview existed. Lists that already existed count as previewed, so
// upgrading never forces a retroactive preview.
func (s *SQLite) ensureListPreviewed() error {
	found, err := s.columnExists("list_states", "previewed")
	if err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE list_states ADD COLUMN previewed INTEGER NOT NULL DEFAULT 1`)
	return err
}

// ensureListGroup adds the suspended-group columns to databases created
// before a group could be paused.
func (s *SQLite) ensureListGroup() error {
	columns := []struct {
		name string
		sql  string
	}{
		{"group", `ALTER TABLE list_states ADD COLUMN "group" TEXT NOT NULL DEFAULT '[]'`},
		{"group_at", `ALTER TABLE list_states ADD COLUMN group_at INTEGER NOT NULL DEFAULT 0`},
	}
	for _, column := range columns {
		found, err := s.columnExists("list_states", column.name)
		if err != nil {
			return err
		}
		if found {
			continue
		}
		if _, err := s.db.Exec(column.sql); err != nil {
			return err
		}
	}
	return nil
}

// healZeroAttemptCompleted repairs lists that an older build marked complete
// when the learner jumped out before answering. The defect left such a list
// with no attempt in any round, so it has no question to answer and cannot
// satisfy the round gate; it returns to the queue.
func (s *SQLite) healZeroAttemptCompleted() error {
	tx, err := s.db.Begin()
	if err != nil {
		return classify(err)
	}
	defer tx.Rollback()

	var round int
	err = tx.QueryRow(`SELECT round FROM rounds ORDER BY round DESC LIMIT 1`).Scan(&round)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return classify(err)
	}

	rows, err := tx.Query(
		`SELECT list_no FROM list_states
		 WHERE round = ? AND completed = 1 AND NOT EXISTS (
			SELECT 1 FROM attempts
			WHERE attempts.list_no = list_states.list_no
		 )
		 ORDER BY list_no`,
		round,
	)
	if err != nil {
		return classify(err)
	}
	var healed []int
	for rows.Next() {
		var listNo int
		if err := rows.Scan(&listNo); err != nil {
			rows.Close()
			return classify(err)
		}
		healed = append(healed, listNo)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return classify(err)
	}
	rows.Close()
	if len(healed) == 0 {
		return tx.Commit()
	}

	for _, listNo := range healed {
		if _, err := tx.Exec(
			`UPDATE list_states SET completed = 0 WHERE round = ? AND list_no = ?`,
			round, listNo,
		); err != nil {
			return classify(err)
		}
	}

	var queueJSON string
	if err := tx.QueryRow(`SELECT queue FROM rounds WHERE round = ?`, round).Scan(&queueJSON); err != nil {
		return classify(err)
	}
	var queue []int
	if err := decode(queueJSON, &queue); err != nil {
		return corrupt(err)
	}
	for _, listNo := range healed {
		if !containsList(queue, listNo) {
			queue = append(queue, listNo)
		}
	}
	data, err := encode(queue)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE rounds SET queue = ? WHERE round = ?`, string(data), round); err != nil {
		return classify(err)
	}
	return classify(tx.Commit())
}

func containsList(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// columnExists reports whether table already has the named column.
func (s *SQLite) columnExists(table, column string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			fallback   sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &fallback, &pk); err != nil {
			return false, err
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return found, nil
}

// Close releases the database handle.
func (s *SQLite) Close() error {
	return s.db.Close()
}

// Load returns the latest round with every recorded attempt.
func (s *SQLite) Load() (engine.State, error) {
	var (
		st    engine.State
		queue string
		quiz  sql.NullString
	)
	err := s.db.QueryRow(
		`SELECT round, seq, queue, current, quiz FROM rounds ORDER BY round DESC LIMIT 1`,
	).Scan(&st.Round, &st.Seq, &queue, &st.Current, &quiz)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.State{}, ErrNoState
	}
	if err != nil {
		return engine.State{}, classify(err)
	}
	if err := decode(queue, &st.Queue); err != nil {
		return engine.State{}, corrupt(err)
	}
	if quiz.Valid {
		var q engine.Quiz
		if err := decode(quiz.String, &q); err != nil {
			return engine.State{}, corrupt(err)
		}
		st.Quiz = &q
	}

	rows, err := s.db.Query(
		`SELECT list_no, pending, first_wrong, completed, previewed, "group", group_at FROM list_states WHERE round = ? ORDER BY list_no`,
		st.Round,
	)
	if err != nil {
		return engine.State{}, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			ls         engine.ListState
			pending    string
			firstWrong string
			group      string
		)
		if err := rows.Scan(&ls.ListNo, &pending, &firstWrong, &ls.Completed, &ls.Previewed, &group, &ls.GroupAt); err != nil {
			return engine.State{}, classify(err)
		}
		if err := decode(pending, &ls.Pending); err != nil {
			return engine.State{}, corrupt(err)
		}
		if err := decode(firstWrong, &ls.FirstWrong); err != nil {
			return engine.State{}, corrupt(err)
		}
		if err := decode(group, &ls.Group); err != nil {
			return engine.State{}, corrupt(err)
		}
		st.Lists = append(st.Lists, ls)
	}
	if err := rows.Err(); err != nil {
		return engine.State{}, classify(err)
	}

	attempts, err := s.db.Query(
		`SELECT round, list_no, question_id, result, via, score, seq FROM attempts ORDER BY round, seq`,
	)
	if err != nil {
		return engine.State{}, classify(err)
	}
	defer attempts.Close()
	for attempts.Next() {
		var (
			listNo int
			result string
			via    string
			a      engine.Attempt
		)
		if err := attempts.Scan(&a.Round, &listNo, &a.QuestionID, &result, &via, &a.Score, &a.Seq); err != nil {
			return engine.State{}, classify(err)
		}
		a.Result = engine.Result(result)
		a.Via = engine.Via(via)
		idx := indexOf(&st, listNo)
		st.Lists[idx].Attempts = append(st.Lists[idx].Attempts, a)
	}
	if err := attempts.Err(); err != nil {
		return engine.State{}, classify(err)
	}
	return st, nil
}

// Save writes the round snapshot and per-list state. Attempts are append-only
// and written by AppendAttempt.
func (s *SQLite) Save(st engine.State) error {
	queue, err := encode(st.Queue)
	if err != nil {
		return err
	}
	var quiz any
	if st.Quiz != nil {
		data, err := encode(st.Quiz)
		if err != nil {
			return err
		}
		quiz = string(data)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return classify(err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`INSERT INTO rounds (round, seq, queue, current, quiz) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(round) DO UPDATE SET
			seq = excluded.seq, queue = excluded.queue,
			current = excluded.current, quiz = excluded.quiz`,
		st.Round, st.Seq, string(queue), st.Current, quiz,
	); err != nil {
		return classify(err)
	}
	for _, ls := range st.Lists {
		pending, err := encode(ls.Pending)
		if err != nil {
			return err
		}
		firstWrong, err := encode(ls.FirstWrong)
		if err != nil {
			return err
		}
		group, err := encode(ls.Group)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO list_states (round, list_no, pending, first_wrong, completed, previewed, "group", group_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(round, list_no) DO UPDATE SET
				pending = excluded.pending, first_wrong = excluded.first_wrong,
				completed = excluded.completed, previewed = excluded.previewed,
				"group" = excluded."group", group_at = excluded.group_at`,
			st.Round, ls.ListNo, string(pending), string(firstWrong), ls.Completed, ls.Previewed,
			string(group), ls.GroupAt,
		); err != nil {
			return classify(err)
		}
	}
	return classify(tx.Commit())
}

// AppendAttempt records one question result.
func (s *SQLite) AppendAttempt(listNo int, a engine.Attempt) error {
	_, err := s.db.Exec(
		`INSERT INTO attempts (round, seq, list_no, question_id, result, via, score)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(round, seq) DO UPDATE SET
			list_no = excluded.list_no, question_id = excluded.question_id,
			result = excluded.result, via = excluded.via, score = excluded.score`,
		a.Round, a.Seq, listNo, a.QuestionID, string(a.Result), string(a.Via), a.Score,
	)
	return classify(err)
}

// exportFile is the JSON backup format. Settings never include device-local
// keys such as the API key or the WebDAV configuration.
type exportFile struct {
	Version  *int              `json:"version"`
	State    *engine.State     `json:"state"`
	Settings map[string]string `json:"settings"`
}

// Export writes a JSON backup of the latest state and non-secret settings.
func (s *SQLite) Export(w io.Writer) error {
	st, err := s.Load()
	if err != nil {
		return err
	}
	settings, err := s.settings()
	if err != nil {
		return err
	}
	version := exportVersion
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(exportFile{Version: &version, State: &st, Settings: settings})
}

// Import replaces the saved state and shared settings with a JSON backup,
// keeping this device's local-only settings. A rejected file leaves existing
// data untouched.
func (s *SQLite) Import(r io.Reader) error {
	var f exportFile
	if err := json.NewDecoder(r).Decode(&f); err != nil {
		return fmt.Errorf("invalid backup: %w", err)
	}
	if f.Version == nil || *f.Version != exportVersion {
		return errors.New("invalid backup: unsupported version")
	}
	if f.State == nil {
		return errors.New("invalid backup: missing state")
	}
	if err := validate(*f.State); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return classify(err)
	}
	defer tx.Rollback()

	// Backups never contain device-local settings (API key, WebDAV config,
	// sync time), so read them now and put them back after the reset.
	local, err := loadLocalSettings(tx)
	if err != nil {
		return err
	}
	for _, table := range []string{"attempts", "list_states", "rounds", "settings"} {
		if _, err := tx.Exec(`DELETE FROM ` + table); err != nil {
			return classify(err)
		}
	}
	st := *f.State
	queue, err := encode(st.Queue)
	if err != nil {
		return err
	}
	var quiz any
	if st.Quiz != nil {
		data, err := encode(st.Quiz)
		if err != nil {
			return err
		}
		quiz = string(data)
	}
	if _, err := tx.Exec(
		`INSERT INTO rounds (round, seq, queue, current, quiz) VALUES (?, ?, ?, ?, ?)`,
		st.Round, st.Seq, string(queue), st.Current, quiz,
	); err != nil {
		return classify(err)
	}
	for _, ls := range st.Lists {
		pending, err := encode(ls.Pending)
		if err != nil {
			return err
		}
		firstWrong, err := encode(ls.FirstWrong)
		if err != nil {
			return err
		}
		group, err := encode(ls.Group)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO list_states (round, list_no, pending, first_wrong, completed, previewed, "group", group_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			st.Round, ls.ListNo, string(pending), string(firstWrong), ls.Completed, ls.Previewed,
			string(group), ls.GroupAt,
		); err != nil {
			return classify(err)
		}
		for _, a := range ls.Attempts {
			if _, err := tx.Exec(
				`INSERT INTO attempts (round, seq, list_no, question_id, result, via, score)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				a.Round, a.Seq, ls.ListNo, a.QuestionID, string(a.Result), string(a.Via), a.Score,
			); err != nil {
				return classify(err)
			}
		}
	}
	for key, value := range f.Settings {
		if isLocalOnly(key) {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, key, value); err != nil {
			return classify(err)
		}
	}
	for key, value := range local {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, key, value); err != nil {
			return classify(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return classify(err)
	}
	// An old backup can carry the same zero-answer completed rows that Open
	// repairs; fix them before the restored state reaches the UI.
	return s.healZeroAttemptCompleted()
}

func (s *SQLite) settings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, classify(err)
		}
		if isLocalOnly(key) {
			continue
		}
		out[key] = value
	}
	return out, rows.Err()
}

func validate(st engine.State) error {
	if st.Round < 1 {
		return errors.New("invalid backup: missing round")
	}
	if st.Lists == nil {
		return errors.New("invalid backup: missing lists")
	}
	if st.Quiz != nil {
		if st.Quiz.ListNo < 1 || st.Quiz.QuestionIDs == nil {
			return errors.New("invalid backup: malformed quiz")
		}
		if st.Quiz.Index < 0 || st.Quiz.Index > len(st.Quiz.QuestionIDs) {
			return errors.New("invalid backup: quiz index out of range")
		}
	}
	for _, ls := range st.Lists {
		if ls.ListNo < 1 {
			return errors.New("invalid backup: malformed list")
		}
		if ls.Completed && len(ls.Group) > 0 {
			return errors.New("invalid backup: completed list has a suspended group")
		}
		if len(ls.Group) == 0 {
			if ls.GroupAt != 0 {
				return errors.New("invalid backup: suspended index without a group")
			}
		} else if ls.GroupAt < 0 || ls.GroupAt >= len(ls.Group) {
			return errors.New("invalid backup: suspended group index out of range")
		}
		for _, id := range ls.Group {
			if id == "" {
				return errors.New("invalid backup: malformed suspended group")
			}
		}
		for _, a := range ls.Attempts {
			if a.Round < 1 || a.Round > st.Round || a.Seq < 1 || a.QuestionID == "" {
				return errors.New("invalid backup: malformed attempt")
			}
			if a.Result != engine.Correct && a.Result != engine.Wrong {
				return errors.New("invalid backup: malformed attempt result")
			}
			switch a.Via {
			case engine.ViaAnswer, engine.ViaLookup, engine.ViaLLM, engine.ViaSelf:
			default:
				return errors.New("invalid backup: malformed attempt source")
			}
			if a.Score < 0 || a.Score > 100 {
				return errors.New("invalid backup: malformed attempt score")
			}
		}
	}
	return nil
}

func indexOf(st *engine.State, listNo int) int {
	for i := range st.Lists {
		if st.Lists[i].ListNo == listNo {
			return i
		}
	}
	st.Lists = append(st.Lists, engine.ListState{ListNo: listNo})
	return len(st.Lists) - 1
}

func encode(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	return data, nil
}

func decode(raw string, v any) error {
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), v)
}

func corrupt(err error) error {
	return fmt.Errorf("%w: %v", ErrCorrupt, err)
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var e *sqlite.Error
	if errors.As(err, &e) {
		switch e.Code() & 0xff {
		case 11, 26: // SQLITE_CORRUPT, SQLITE_NOTADB
			return corrupt(err)
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "not a database") || strings.Contains(msg, "malformed") {
		return corrupt(err)
	}
	return err
}
