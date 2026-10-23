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
		PRIMARY KEY (round, list_no)
	)`,
	`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
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
	return s.ensureAttemptScore()
}

// ensureAttemptScore adds the score column to databases created before grades
// were stored. Old attempts keep the 0 default.
func (s *SQLite) ensureAttemptScore() error {
	rows, err := s.db.Query(`PRAGMA table_info(attempts)`)
	if err != nil {
		return err
	}
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
			rows.Close()
			return err
		}
		if name == "score" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if found {
		return nil
	}
	_, err = s.db.Exec(`ALTER TABLE attempts ADD COLUMN score INTEGER NOT NULL DEFAULT 0`)
	return err
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
		`SELECT list_no, pending, first_wrong, completed FROM list_states WHERE round = ? ORDER BY list_no`,
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
		)
		if err := rows.Scan(&ls.ListNo, &pending, &firstWrong, &ls.Completed); err != nil {
			return engine.State{}, classify(err)
		}
		if err := decode(pending, &ls.Pending); err != nil {
			return engine.State{}, corrupt(err)
		}
		if err := decode(firstWrong, &ls.FirstWrong); err != nil {
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
		if _, err := tx.Exec(
			`INSERT INTO list_states (round, list_no, pending, first_wrong, completed)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(round, list_no) DO UPDATE SET
				pending = excluded.pending, first_wrong = excluded.first_wrong,
				completed = excluded.completed`,
			st.Round, ls.ListNo, string(pending), string(firstWrong), ls.Completed,
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
		if _, err := tx.Exec(
			`INSERT INTO list_states (round, list_no, pending, first_wrong, completed)
			 VALUES (?, ?, ?, ?, ?)`,
			st.Round, ls.ListNo, string(pending), string(firstWrong), ls.Completed,
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
	return classify(tx.Commit())
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
