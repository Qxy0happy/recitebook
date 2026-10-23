package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"recitebook/internal/engine"
)

func openTemp(t *testing.T) (*SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func sampleState() engine.State {
	return engine.State{
		Round:   2,
		Queue:   []int{3, 1},
		Current: 3,
		Quiz: &engine.Quiz{
			ListNo:      3,
			QuestionIDs: []string{"q1", "q2", "q3"},
			Index:       1,
		},
		Seq: 7,
		Lists: []engine.ListState{
			{
				ListNo:     1,
				Pending:    []string{"q2", "q4"},
				FirstWrong: []string{"q2"},
				Completed:  false,
				Attempts: []engine.Attempt{
					{Round: 1, QuestionID: "q1", Result: engine.Correct, Via: engine.ViaAnswer, Score: 100, Seq: 1},
					{Round: 2, QuestionID: "q1", Result: engine.Wrong, Via: engine.ViaLookup, Score: 0, Seq: 5},
					{Round: 2, QuestionID: "q2", Result: engine.Correct, Via: engine.ViaLLM, Score: 85, Seq: 6},
				},
			},
			{
				ListNo:  3,
				Pending: []string{},
				Attempts: []engine.Attempt{
					{Round: 2, QuestionID: "q1", Result: engine.Correct, Via: engine.ViaSelf, Score: 80, Seq: 7},
				},
			},
		},
	}
}

func TestRoundTripAcrossReopen(t *testing.T) {
	s, path := openTemp(t)
	want := sampleState()
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, ls := range want.Lists {
		for _, a := range ls.Attempts {
			if err := s.AppendAttempt(ls.ListNo, a); err != nil {
				t.Fatalf("AppendAttempt: %v", err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestLoadWithoutState(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.Load(); !errors.Is(err, ErrNoState) {
		t.Fatalf("Load error = %v, want ErrNoState", err)
	}
}

func TestExportOmitsAPIKeyAndImportRestores(t *testing.T) {
	s, _ := openTemp(t)
	state := sampleState()
	if err := s.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, ls := range state.Lists {
		for _, a := range ls.Attempts {
			if err := s.AppendAttempt(ls.ListNo, a); err != nil {
				t.Fatalf("AppendAttempt: %v", err)
			}
		}
	}
	if _, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?), (?, ?)`,
		SettingAPIKey, "sk-secret", "base_url", "https://example.test",
	); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	var backup bytes.Buffer
	if err := s.Export(&backup); err != nil {
		t.Fatalf("Export: %v", err)
	}
	text := backup.String()
	if strings.Contains(text, "sk-secret") || strings.Contains(text, SettingAPIKey) {
		t.Fatalf("export leaked API key: %s", text)
	}
	if !strings.Contains(text, "https://example.test") {
		t.Fatalf("export dropped normal setting: %s", text)
	}

	target, _ := openTemp(t)
	if _, err := target.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)`, SettingAPIKey, "local-key",
	); err != nil {
		t.Fatalf("seed local key: %v", err)
	}
	if err := target.Import(bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatalf("Import: %v", err)
	}
	got, err := target.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("imported state mismatch:\ngot  %+v\nwant %+v", got, state)
	}
	settings, err := target.settings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	var localKey string
	if err := target.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, SettingAPIKey).Scan(&localKey); err != nil {
		t.Fatalf("local API key: %v", err)
	}
	if localKey != "local-key" {
		t.Fatalf("import dropped local API key: %q", localKey)
	}
	if settings["base_url"] != "https://example.test" {
		t.Fatalf("import lost setting: %v", settings)
	}
}

func TestImportRejectsBadFilesWithoutChangingData(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.Save(sampleState()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	want, err := s.Load()
	if err != nil {
		t.Fatalf("Load baseline: %v", err)
	}

	bad := map[string]string{
		"not json":        `{`,
		"missing version": `{"state":` + mustJSON(t, want) + `}`,
		"missing state":   `{"version":1}`,
		"wrong version":   `{"version":9,"state":` + mustJSON(t, want) + `}`,
		"no round":        `{"version":1,"state":{}}`,
		"bad result": `{"version":1,"state":{"round":1,"queue":[],"lists":[` +
			`{"list_no":1,"attempts":[{"round":1,"question_id":"q","result":"maybe","via":"answer","score":100,"seq":1}]}]}}`,
		"bad score": `{"version":1,"state":{"round":1,"queue":[],"lists":[` +
			`{"list_no":1,"attempts":[{"round":1,"question_id":"q","result":"wrong","via":"answer","score":101,"seq":1}]}]}}`,
		"bad source": `{"version":1,"state":{"round":1,"queue":[],"lists":[` +
			`{"list_no":1,"attempts":[{"round":1,"question_id":"q","result":"wrong","via":"guess","score":0,"seq":1}]}]}}`,
	}
	for name, payload := range bad {
		if err := s.Import(strings.NewReader(payload)); err == nil {
			t.Fatalf("%s: Import succeeded, want error", name)
		}
		got, err := s.Load()
		if err != nil {
			t.Fatalf("%s: Load after rejected import: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: rejected import changed data", name)
		}
	}
}

func TestOpenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, []byte("this is not a sqlite database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open error = %v, want ErrCorrupt", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExportUsesSnakeCaseKeys(t *testing.T) {
	s, _ := openTemp(t)
	state := sampleState()
	if err := s.Save(state); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, ls := range state.Lists {
		for _, a := range ls.Attempts {
			if err := s.AppendAttempt(ls.ListNo, a); err != nil {
				t.Fatalf("AppendAttempt: %v", err)
			}
		}
	}
	var backup bytes.Buffer
	if err := s.Export(&backup); err != nil {
		t.Fatalf("Export: %v", err)
	}
	text := backup.String()
	for _, key := range []string{`"list_no"`, `"question_id"`, `"question_ids"`, `"first_wrong"`, `"score"`} {
		if !strings.Contains(text, key) {
			t.Fatalf("export is missing %s: %s", key, text)
		}
	}
	for _, key := range []string{`"listNo"`, `"questionId"`, `"questionIds"`, `"firstWrong"`} {
		if strings.Contains(text, key) {
			t.Fatalf("export still uses camelCase %s: %s", key, text)
		}
	}
}

func TestMigrateAddsScoreColumnToExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	for _, statement := range []string{
		`CREATE TABLE attempts (
			round INTEGER NOT NULL,
			seq INTEGER NOT NULL,
			list_no INTEGER NOT NULL,
			question_id TEXT NOT NULL,
			result TEXT NOT NULL,
			via TEXT NOT NULL,
			PRIMARY KEY (round, seq)
		)`,
		`INSERT INTO attempts (round, seq, list_no, question_id, result, via)
		 VALUES (1, 1, 1, 'q1', 'correct', 'answer')`,
		`CREATE TABLE rounds (round INTEGER PRIMARY KEY, seq INTEGER NOT NULL,
			queue TEXT NOT NULL, current INTEGER NOT NULL, quiz TEXT)`,
		`INSERT INTO rounds (round, seq, queue, current, quiz) VALUES (1, 1, '[1]', 0, NULL)`,
		`CREATE TABLE list_states (round INTEGER NOT NULL, list_no INTEGER NOT NULL,
			pending TEXT NOT NULL DEFAULT '[]', first_wrong TEXT NOT NULL DEFAULT '[]',
			completed INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (round, list_no))`,
		`INSERT INTO list_states (round, list_no, pending, first_wrong, completed)
		 VALUES (1, 1, '[]', '[]', 1)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("seed legacy database: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy database: %v", err)
	}
	defer s.Close()
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load legacy database: %v", err)
	}
	if len(got.Lists) != 1 || len(got.Lists[0].Attempts) != 1 {
		t.Fatalf("legacy attempts = %+v", got.Lists)
	}
	if score := got.Lists[0].Attempts[0].Score; score != 0 {
		t.Fatalf("legacy attempt score = %d, want the 0 default", score)
	}

	scored := engine.Attempt{Round: 1, QuestionID: "q2", Result: engine.Correct, Via: engine.ViaLLM, Score: 73, Seq: 2}
	if err := s.AppendAttempt(1, scored); err != nil {
		t.Fatalf("AppendAttempt after migration: %v", err)
	}
	got, err = s.Load()
	if err != nil {
		t.Fatalf("Load after migration: %v", err)
	}
	attempts := got.Lists[0].Attempts
	if len(attempts) != 2 || attempts[1].Score != 73 || attempts[1].Via != engine.ViaLLM {
		t.Fatalf("migrated attempts = %+v", attempts)
	}
}
