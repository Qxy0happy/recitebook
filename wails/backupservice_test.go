package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"recitebook/internal/engine"
	"recitebook/internal/store"
)

func newTestEngine(t *testing.T) (*BankService, *EngineService) {
	t.Helper()
	bank, err := NewBankService()
	if err != nil {
		t.Fatalf("NewBankService: %v", err)
	}
	svc, _, warning, err := openEngine(bank, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	t.Cleanup(func() { closeEngine(t, svc) })
	return bank, svc
}

func roundTripState(t *testing.T, svc *EngineService) engine.State {
	t.Helper()
	if _, err := svc.Enter(1); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	state, err := svc.Answer(false)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	return state
}

func TestBackupExportContainsProgressAndOmitsAPIKey(t *testing.T) {
	_, svc := newTestEngine(t)
	state := roundTripState(t, svc)

	db, ok := svc.db.(store.SettingsStore)
	if !ok {
		t.Fatal("engine store does not expose settings")
	}
	if _, err := NewGradeService(db).SaveSettings("https://example.test", "gpt-test", "sk-secret"); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	path := filepath.Join(t.TempDir(), "backup.json")
	if err := NewBackupService(svc).Export(path); err != nil {
		t.Fatalf("Export: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	text := string(raw)
	if strings.Contains(text, "sk-secret") || strings.Contains(text, store.SettingAPIKey) {
		t.Fatalf("export leaked API key: %s", text)
	}
	if !strings.Contains(text, "https://example.test") {
		t.Fatalf("export dropped settings: %s", text)
	}

	var backup struct {
		Version  int               `json:"version"`
		State    engine.State      `json:"state"`
		Settings map[string]string `json:"settings"`
	}
	if err := json.Unmarshal(raw, &backup); err != nil {
		t.Fatalf("backup is not valid JSON: %v", err)
	}
	if backup.Version != 1 {
		t.Fatalf("version = %d, want 1", backup.Version)
	}
	if !reflect.DeepEqual(backup.State, state) {
		t.Fatalf("backup state mismatch:\ngot  %+v\nwant %+v", backup.State, state)
	}
	attempts := backup.State.Lists[0].Attempts
	if len(attempts) != 1 || attempts[0].QuestionID != state.Lists[0].Attempts[0].QuestionID {
		t.Fatalf("attempts not exported: %+v", attempts)
	}
	if len(backup.State.Lists[0].Pending) == 0 {
		t.Fatalf("pending snapshot not exported: %+v", backup.State.Lists[0])
	}
	if len(backup.State.Lists[0].FirstWrong) == 0 {
		t.Fatalf("first-wrong snapshot not exported: %+v", backup.State.Lists[0])
	}
}

func TestBackupImportReloadsEngineState(t *testing.T) {
	_, source := newTestEngine(t)
	sourceState := roundTripState(t, source)
	path := filepath.Join(t.TempDir(), "backup.json")
	if err := NewBackupService(source).Export(path); err != nil {
		t.Fatalf("Export: %v", err)
	}

	_, target := newTestEngine(t)
	if _, err := target.Enter(1); err != nil {
		t.Fatalf("target Enter: %v", err)
	}
	if _, err := target.Answer(true); err != nil {
		t.Fatalf("target Answer: %v", err)
	}
	if reflect.DeepEqual(target.State(), sourceState) {
		t.Fatal("target state already matches source")
	}

	backup := NewBackupService(target)
	got, err := backup.Import(path)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !reflect.DeepEqual(got, sourceState) {
		t.Fatalf("imported state mismatch:\ngot  %+v\nwant %+v", got, sourceState)
	}
	if !reflect.DeepEqual(target.State(), sourceState) {
		t.Fatalf("engine state not reloaded after import:\ngot  %+v\nwant %+v", target.State(), sourceState)
	}
}

func TestBackupImportRejectsBadFilesWithoutChangingData(t *testing.T) {
	_, svc := newTestEngine(t)
	roundTripState(t, svc)
	want := svc.State()
	path := filepath.Join(t.TempDir(), "bad.json")

	bad := map[string]string{
		"not json":        `{`,
		"missing version": `{"state":{"round":1}}`,
		"missing state":   `{"version":1}`,
		"empty state":     `{"version":1,"state":{}}`,
	}
	backup := NewBackupService(svc)
	for name, payload := range bad {
		if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
			t.Fatal(err)
		}
		state, err := backup.Import(path)
		if err == nil {
			t.Fatalf("%s: Import succeeded, want error", name)
		}
		if !strings.Contains(err.Error(), "导入失败") {
			t.Fatalf("%s: error is not user-facing: %v", name, err)
		}
		if !reflect.DeepEqual(state, want) {
			t.Fatalf("%s: rejected import changed returned state", name)
		}
		if !reflect.DeepEqual(svc.State(), want) {
			t.Fatalf("%s: rejected import changed engine state", name)
		}
	}
}

func TestBackupImportMissingFileLeavesStateAlone(t *testing.T) {
	_, svc := newTestEngine(t)
	roundTripState(t, svc)
	want := svc.State()

	if _, err := NewBackupService(svc).Import(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("Import of missing file succeeded, want error")
	}
	if !reflect.DeepEqual(svc.State(), want) {
		t.Fatal("failed import changed engine state")
	}
}
