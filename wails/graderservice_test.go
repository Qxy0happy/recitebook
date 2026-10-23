package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"recitebook/internal/engine"
	"recitebook/internal/grader"
	"recitebook/internal/store"
)

func openGradeService(t *testing.T) (*GradeService, *store.SQLite) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewGradeService(db), db
}

func TestGradeServiceEchoesSettingsWithoutKey(t *testing.T) {
	svc, db := openGradeService(t)

	view, err := svc.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if view.BaseURL != "" || view.Model != "" || view.APIKeySet {
		t.Fatalf("fresh view = %+v", view)
	}

	saved, err := svc.SaveSettings(" https://api.example.test/v1 ", " gpt-test ", "sk-secret")
	if err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if saved.BaseURL != "https://api.example.test/v1" || saved.Model != "gpt-test" || !saved.APIKeySet {
		t.Fatalf("saved view = %+v", saved)
	}

	echoed, err := svc.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if echoed != saved {
		t.Fatalf("echoed view = %+v, want %+v", echoed, saved)
	}

	stored, err := db.LoadLLMSettings()
	if err != nil {
		t.Fatalf("LoadLLMSettings: %v", err)
	}
	if stored.APIKey != "sk-secret" {
		t.Fatalf("stored API key = %q", stored.APIKey)
	}
}

func TestGradeServiceBlankKeyKeepsSavedKey(t *testing.T) {
	svc, db := openGradeService(t)
	if _, err := svc.SaveSettings("https://api.example.test/v1", "gpt-test", "sk-secret"); err != nil {
		t.Fatalf("first save: %v", err)
	}
	view, err := svc.SaveSettings("https://api.example.test/v1", "gpt-next", "")
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if !view.APIKeySet || view.Model != "gpt-next" {
		t.Fatalf("view = %+v", view)
	}
	stored, err := db.LoadLLMSettings()
	if err != nil {
		t.Fatalf("LoadLLMSettings: %v", err)
	}
	if stored.APIKey != "sk-secret" {
		t.Fatalf("API key was not preserved: %q", stored.APIKey)
	}
}

func TestGradeServiceGradesWithStoredSettings(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{
					"content": `{"score": 82, "issues": ["漏译"], "comment": "尚可"}`,
				},
			}},
		})
	}))
	defer srv.Close()

	svc, _ := openGradeService(t)
	if _, err := svc.SaveSettings(srv.URL+"/v1", "gpt-test", "sk-secret"); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := svc.Grade("L1-09", "Companies rely on artificial intelligence.", "公司依靠人工智能。", "公司依赖人工智能。")
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if got.Score != 82 || !got.Correct || len(got.Issues) != 1 || got.Comment != "尚可" {
		t.Fatalf("result = %+v", got)
	}
}

func TestGradeServiceWithoutKeyReturnsRecognizableError(t *testing.T) {
	svc, _ := openGradeService(t)
	_, err := svc.Grade("L1-09", "stem", "answer", "reference")
	if !errors.Is(err, grader.ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
}

func TestAnswerSelfRecordsSelfSource(t *testing.T) {
	bank, err := NewBankService()
	if err != nil {
		t.Fatal(err)
	}
	svc, _, _, err := openEngine(bank, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	defer closeEngine(t, svc)
	if _, err := svc.Enter(1); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	got, err := svc.AnswerSelf(false, 50)
	if err != nil {
		t.Fatalf("AnswerSelf: %v", err)
	}
	attempts := got.Lists[0].Attempts
	if len(attempts) != 1 || attempts[0].Via != engine.ViaSelf || attempts[0].Score != 50 {
		t.Fatalf("attempts = %+v", attempts)
	}
}
