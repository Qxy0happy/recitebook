package store

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"recitebook/internal/engine"
)

func TestLLMSettingsRoundTrip(t *testing.T) {
	s, path := openTemp(t)
	want := LLMSettings{
		BaseURL: "https://api.example.test/v1",
		Model:   "gpt-test",
		APIKey:  "sk-secret",
	}
	if err := s.SaveLLMSettings(want); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	got, err := s.LoadLLMSettings()
	if err != nil {
		t.Fatalf("LoadLLMSettings: %v", err)
	}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	got, err = reopened.LoadLLMSettings()
	if err != nil {
		t.Fatalf("LoadLLMSettings after reopen: %v", err)
	}
	if got != want {
		t.Fatalf("settings after reopen = %+v, want %+v", got, want)
	}
}

func TestLLMSettingsEmptyByDefault(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.LoadLLMSettings()
	if err != nil {
		t.Fatalf("LoadLLMSettings: %v", err)
	}
	if got != (LLMSettings{}) {
		t.Fatalf("settings = %+v, want zero value", got)
	}
}

func TestLLMSettingsSaveReplaces(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.SaveLLMSettings(LLMSettings{BaseURL: "https://a.test/v1", Model: "m1", APIKey: "k1"}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := s.SaveLLMSettings(LLMSettings{BaseURL: "https://b.test/v1", Model: "m2", APIKey: "k2"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := s.LoadLLMSettings()
	if err != nil {
		t.Fatalf("LoadLLMSettings: %v", err)
	}
	if got.BaseURL != "https://b.test/v1" || got.Model != "m2" || got.APIKey != "k2" {
		t.Fatalf("settings = %+v", got)
	}
}

func TestExportOmitsLLMSettingsAPIKey(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.Save(engine.New([]int{1})); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.SaveLLMSettings(LLMSettings{BaseURL: "https://a.test/v1", Model: "m1", APIKey: "sk-secret"}); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}

	var backup bytes.Buffer
	if err := s.Export(&backup); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if strings.Contains(backup.String(), "sk-secret") {
		t.Fatalf("export leaks the API key: %s", backup.String())
	}
	var file exportFile
	if err := json.Unmarshal(backup.Bytes(), &file); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if file.Settings["llm_base_url"] != "https://a.test/v1" || file.Settings["llm_model"] != "m1" {
		t.Fatalf("non-secret settings missing: %+v", file.Settings)
	}
}
