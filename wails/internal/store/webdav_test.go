package store

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"recitebook/internal/engine"
)

func TestWebDAVSettingsRoundTrip(t *testing.T) {
	s, path := openTemp(t)
	want := WebDAVSettings{
		URL:      "https://dav.example.test/remote.php/dav",
		Username: "alice@example.test",
		Password: "app-password",
	}
	if err := s.SaveWebDAVSettings(want); err != nil {
		t.Fatalf("SaveWebDAVSettings: %v", err)
	}
	got, err := s.LoadWebDAVSettings()
	if err != nil {
		t.Fatalf("LoadWebDAVSettings: %v", err)
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
	got, err = reopened.LoadWebDAVSettings()
	if err != nil {
		t.Fatalf("LoadWebDAVSettings after reopen: %v", err)
	}
	if got != want {
		t.Fatalf("settings after reopen = %+v, want %+v", got, want)
	}
}

func TestWebDAVSettingsEmptyByDefault(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.LoadWebDAVSettings()
	if err != nil {
		t.Fatalf("LoadWebDAVSettings: %v", err)
	}
	if got != (WebDAVSettings{}) {
		t.Fatalf("settings = %+v, want zero value", got)
	}
}

func TestSyncLastAtRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := s.SaveSyncLastAt(want); err != nil {
		t.Fatalf("SaveSyncLastAt: %v", err)
	}
	got, err := s.LoadSyncLastAt()
	if err != nil {
		t.Fatalf("LoadSyncLastAt: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("sync time = %v, want %v", got, want)
	}
}

func TestLoadSyncLastAtZeroWhenUnset(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.LoadSyncLastAt()
	if err != nil {
		t.Fatalf("LoadSyncLastAt: %v", err)
	}
	if !got.IsZero() {
		t.Fatalf("sync time = %v, want zero value", got)
	}
}

func TestIsLocalOnly(t *testing.T) {
	for _, key := range []string{
		SettingAPIKey,
		SettingWebDAVURL,
		SettingWebDAVUsername,
		SettingWebDAVPassword,
		SettingSyncLastAt,
	} {
		if !isLocalOnly(key) {
			t.Errorf("isLocalOnly(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"llm_base_url", "llm_model", "base_url", "webdav", "sync"} {
		if isLocalOnly(key) {
			t.Errorf("isLocalOnly(%q) = true, want false", key)
		}
	}
}

func TestExportOmitsWebDAVConfig(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.Save(engine.New([]int{1})); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.SaveLLMSettings(LLMSettings{BaseURL: "https://api.test/v1", Model: "m1", APIKey: "sk-secret"}); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	if err := s.SaveWebDAVSettings(WebDAVSettings{
		URL:      "https://dav.example.test/dav",
		Username: "alice",
		Password: "dav-secret",
	}); err != nil {
		t.Fatalf("SaveWebDAVSettings: %v", err)
	}
	if err := s.SaveSyncLastAt(time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)); err != nil {
		t.Fatalf("SaveSyncLastAt: %v", err)
	}

	var backup bytes.Buffer
	if err := s.Export(&backup); err != nil {
		t.Fatalf("Export: %v", err)
	}
	text := backup.String()
	for _, secret := range []string{
		SettingWebDAVURL,
		SettingWebDAVUsername,
		SettingWebDAVPassword,
		SettingSyncLastAt,
		"dav-secret",
		"dav.example.test",
	} {
		if strings.Contains(text, secret) {
			t.Fatalf("export contains %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "https://api.test/v1") {
		t.Fatalf("export dropped normal settings: %s", text)
	}
}

func TestImportKeepsLocalWebDAVConfig(t *testing.T) {
	source, _ := openTemp(t)
	if err := source.Save(engine.New([]int{1})); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := source.SaveLLMSettings(LLMSettings{
		BaseURL: "https://file.test/v1",
		Model:   "file-model",
		APIKey:  "file-key",
	}); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	var backup bytes.Buffer
	if err := source.Export(&backup); err != nil {
		t.Fatalf("Export: %v", err)
	}

	target, _ := openTemp(t)
	localDAV := WebDAVSettings{
		URL:      "https://local.test/dav",
		Username: "local-user",
		Password: "local-password",
	}
	if err := target.SaveLLMSettings(LLMSettings{
		BaseURL: "https://local.test/v1",
		Model:   "local-model",
		APIKey:  "local-key",
	}); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	if err := target.SaveWebDAVSettings(localDAV); err != nil {
		t.Fatalf("SaveWebDAVSettings: %v", err)
	}
	localSync := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if err := target.SaveSyncLastAt(localSync); err != nil {
		t.Fatalf("SaveSyncLastAt: %v", err)
	}

	if err := target.Import(bytes.NewReader(backup.Bytes())); err != nil {
		t.Fatalf("Import: %v", err)
	}

	gotDAV, err := target.LoadWebDAVSettings()
	if err != nil {
		t.Fatalf("LoadWebDAVSettings: %v", err)
	}
	if gotDAV != localDAV {
		t.Fatalf("import changed local WebDAV settings: %+v", gotDAV)
	}
	gotSync, err := target.LoadSyncLastAt()
	if err != nil {
		t.Fatalf("LoadSyncLastAt: %v", err)
	}
	if !gotSync.Equal(localSync) {
		t.Fatalf("import changed local sync time: %v", gotSync)
	}
	gotLLM, err := target.LoadLLMSettings()
	if err != nil {
		t.Fatalf("LoadLLMSettings: %v", err)
	}
	if gotLLM.APIKey != "local-key" {
		t.Fatalf("import changed local API key: %q", gotLLM.APIKey)
	}
	if gotLLM.BaseURL != "https://file.test/v1" || gotLLM.Model != "file-model" {
		t.Fatalf("import did not apply file settings: %+v", gotLLM)
	}

	settings, err := target.settings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	for key := range settings {
		if isLocalOnly(key) {
			t.Fatalf("settings() exposed local-only key %q", key)
		}
	}
}

func TestImportIgnoresLocalOnlyKeysFromFile(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.Save(engine.New([]int{1})); err != nil {
		t.Fatalf("Save: %v", err)
	}
	localDAV := WebDAVSettings{URL: "https://local.test/dav", Username: "u", Password: "p"}
	if err := s.SaveWebDAVSettings(localDAV); err != nil {
		t.Fatalf("SaveWebDAVSettings: %v", err)
	}

	var backup bytes.Buffer
	if err := s.Export(&backup); err != nil {
		t.Fatalf("Export: %v", err)
	}
	const injected = `"settings": {"webdav_url": "https://evil.test/dav", "llm_base_url": "https://file.test/v1"}`
	tampered := strings.Replace(backup.String(), `"settings": {}`, injected, 1)
	if tampered == backup.String() {
		t.Fatalf("could not tamper backup: %s", backup.String())
	}
	if err := s.Import(strings.NewReader(tampered)); err != nil {
		t.Fatalf("Import: %v", err)
	}
	got, err := s.LoadWebDAVSettings()
	if err != nil {
		t.Fatalf("LoadWebDAVSettings: %v", err)
	}
	if got != localDAV {
		t.Fatalf("import applied file WebDAV config: %+v", got)
	}
	if st, err := s.LoadLLMSettings(); err != nil || st.BaseURL != "https://file.test/v1" {
		t.Fatalf("file settings not applied: %+v, %v", st, err)
	}
}
