package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"recitebook/internal/store"
)

const syncTestPassword = "app-password-秘密"

var syncTestNow = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

// fakeDAV is an in-memory WebDAV file server. It records the request order and
// keeps the uploaded bytes so a download can serve them back.
type fakeDAV struct {
	mu     sync.Mutex
	calls  []string
	data   []byte
	exists bool

	mkcolErr error
	putErr   error
	getErr   error

	delay       time.Duration
	inFlight    int
	maxInFlight int
}

func (f *fakeDAV) begin(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
}

func (f *fakeDAV) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight--
}

func (f *fakeDAV) EnsureFolder(context.Context) error {
	f.begin("MKCOL")
	defer f.end()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.mkcolErr
}

func (f *fakeDAV) Put(_ context.Context, data []byte) error {
	f.begin("PUT")
	defer f.end()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.putErr != nil {
		return f.putErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = append([]byte(nil), data...)
	f.exists = true
	return nil
}

func (f *fakeDAV) Get(context.Context) ([]byte, bool, error) {
	f.begin("GET")
	defer f.end()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.getErr != nil {
		return nil, false, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists {
		return nil, false, nil
	}
	return append([]byte(nil), f.data...), true, nil
}

func (f *fakeDAV) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeDAV) lastPut() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.data...)
}

// newSyncEngine opens a real temporary SQLite database and engine.
func newSyncEngine(t *testing.T) (*EngineService, *store.SQLite) {
	t.Helper()
	bank, err := NewBankService()
	if err != nil {
		t.Fatalf("NewBankService: %v", err)
	}
	svc, db, warning, err := openEngine(bank, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("openEngine: %v", err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	t.Cleanup(func() { closeEngine(t, svc) })
	return svc, db
}

func newSyncService(engine *EngineService, db *store.SQLite, fake *fakeDAV, now func() time.Time) *SyncService {
	svc := NewSyncService(engine, db)
	svc.newClient = func(store.WebDAVSettings) (davClient, error) { return fake, nil }
	svc.now = now
	return svc
}

func fixedNow() time.Time { return syncTestNow }

func mustConfigureSync(t *testing.T, svc *SyncService) {
	t.Helper()
	view, err := svc.SaveSettings(
		"https://dav.jianguoyun.com/dav/recitebook/progress.json",
		"me@example.com",
		syncTestPassword,
	)
	if err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if !view.PasswordSet {
		t.Fatalf("PasswordSet = false after saving a password")
	}
}

func TestSyncSettingsTrimsAndKeepsPassword(t *testing.T) {
	_, db := newSyncEngine(t)
	svc := newSyncService(nil, db, &fakeDAV{}, fixedNow)

	view, err := svc.SaveSettings(
		"  https://dav.jianguoyun.com/dav/recitebook/progress.json  ",
		"  me@example.com  ",
		"first-password",
	)
	if err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if view.URL != "https://dav.jianguoyun.com/dav/recitebook/progress.json" {
		t.Fatalf("url = %q, want trimmed", view.URL)
	}
	if view.Username != "me@example.com" {
		t.Fatalf("username = %q, want trimmed", view.Username)
	}
	if !view.PasswordSet {
		t.Fatal("PasswordSet = false, want true")
	}
	if view.LastSyncedAt != "" {
		t.Fatalf("last_synced_at = %q, want empty before any sync", view.LastSyncedAt)
	}

	// An empty password keeps the stored one and is never echoed back.
	view, err = svc.SaveSettings(
		"https://dav.jianguoyun.com/dav/other.json",
		"me@example.com",
		"",
	)
	if err != nil {
		t.Fatalf("SaveSettings without password: %v", err)
	}
	if view.URL != "https://dav.jianguoyun.com/dav/other.json" {
		t.Fatalf("url = %q, want updated", view.URL)
	}
	if !view.PasswordSet {
		t.Fatal("PasswordSet = false after keeping the old password")
	}
	if strings.Contains(view.URL, "first-password") || strings.Contains(view.Username, "first-password") {
		t.Fatal("view leaks the password")
	}
	stored, err := db.LoadWebDAVSettings()
	if err != nil {
		t.Fatalf("LoadWebDAVSettings: %v", err)
	}
	if stored.Password != "first-password" {
		t.Fatalf("stored password = %q, want the previous one", stored.Password)
	}
}

func TestSyncSettingsRejectsDirectoryURL(t *testing.T) {
	_, db := newSyncEngine(t)
	svc := newSyncService(nil, db, &fakeDAV{}, fixedNow)

	for _, raw := range []string{
		"",
		"https://dav.jianguoyun.com/dav/recitebook/",
		"ftp://dav.example.com/progress.json",
	} {
		if _, err := svc.SaveSettings(raw, "me@example.com", "pw"); err == nil {
			t.Fatalf("SaveSettings(%q) = nil error, want rejection", raw)
		}
	}
	stored, err := db.LoadWebDAVSettings()
	if err != nil {
		t.Fatalf("LoadWebDAVSettings: %v", err)
	}
	if stored != (store.WebDAVSettings{}) {
		t.Fatalf("invalid saves changed settings: %+v", stored)
	}
}

func TestSyncUploadCreatesFolderThenPutsExportedBytes(t *testing.T) {
	engine, db := newSyncEngine(t)
	if _, err := engine.Enter(1); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if _, err := engine.Answer(false); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	fake := &fakeDAV{}
	svc := newSyncService(engine, db, fake, fixedNow)
	mustConfigureSync(t, svc)

	var want bytes.Buffer
	if err := db.Export(&want); err != nil {
		t.Fatalf("Export: %v", err)
	}

	res, err := svc.Upload()
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if got := fake.callLog(); !reflect.DeepEqual(got, []string{"MKCOL", "PUT"}) {
		t.Fatalf("requests = %v, want MKCOL before PUT", got)
	}
	if got := fake.lastPut(); !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("uploaded bytes differ from Export:\ngot  %s\nwant %s", got, want.Bytes())
	}
	if res.SyncedAt != syncTestNow.Format(time.RFC3339) {
		t.Fatalf("synced_at = %q, want %q", res.SyncedAt, syncTestNow.Format(time.RFC3339))
	}
	at, err := db.LoadSyncLastAt()
	if err != nil {
		t.Fatalf("LoadSyncLastAt: %v", err)
	}
	if !at.Equal(syncTestNow) {
		t.Fatalf("sync_last_at = %v, want %v", at, syncTestNow)
	}
	view, err := svc.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if view.LastSyncedAt != syncTestNow.Format(time.RFC3339) {
		t.Fatalf("last_synced_at = %q", view.LastSyncedAt)
	}
}

// TestSyncUploadContinuesWhenMKCOLReturns405 drives the real webdav client
// against a fake server whose MKCOL answers 405 because the folder exists.
func TestSyncUploadContinuesWhenMKCOLReturns405(t *testing.T) {
	engine, db := newSyncEngine(t)
	if _, err := engine.Enter(1); err != nil {
		t.Fatalf("Enter: %v", err)
	}

	var mu sync.Mutex
	var methods []string
	var uploaded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			mu.Lock()
			uploaded = append([]byte(nil), body...)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	svc := NewSyncService(engine, db)
	if _, err := svc.SaveSettings(srv.URL+"/dav/recitebook/progress.json", "me@example.com", syncTestPassword); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if _, err := svc.Upload(); err != nil {
		t.Fatalf("Upload with MKCOL 405: %v", err)
	}

	var want bytes.Buffer
	if err := db.Export(&want); err != nil {
		t.Fatalf("Export: %v", err)
	}
	mu.Lock()
	gotMethods := append([]string(nil), methods...)
	gotUploaded := append([]byte(nil), uploaded...)
	mu.Unlock()
	if !reflect.DeepEqual(gotMethods, []string{"MKCOL", "PUT"}) {
		t.Fatalf("requests = %v, want MKCOL then PUT", gotMethods)
	}
	if !bytes.Equal(gotUploaded, want.Bytes()) {
		t.Fatalf("uploaded bytes differ from Export")
	}
}

func TestSyncUploadFailuresKeepLastSyncedAt(t *testing.T) {
	old := syncTestNow.Add(-24 * time.Hour)
	cases := []struct {
		name  string
		fake  *fakeDAV
		calls []string
	}{
		{
			name:  "mkcol fails",
			fake:  &fakeDAV{mkcolErr: errors.New("WebDAV 创建目录失败（HTTP 409）")},
			calls: []string{"MKCOL"},
		},
		{
			name:  "put fails",
			fake:  &fakeDAV{putErr: errors.New("WebDAV 上传失败（HTTP 500）")},
			calls: []string{"MKCOL", "PUT"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine, db := newSyncEngine(t)
			if err := db.SaveSyncLastAt(old); err != nil {
				t.Fatalf("SaveSyncLastAt: %v", err)
			}
			svc := newSyncService(engine, db, tc.fake, fixedNow)
			mustConfigureSync(t, svc)

			if _, err := svc.Upload(); err == nil {
				t.Fatal("Upload = nil error, want failure")
			}
			if got := tc.fake.callLog(); !reflect.DeepEqual(got, tc.calls) {
				t.Fatalf("requests = %v, want %v", got, tc.calls)
			}
			at, err := db.LoadSyncLastAt()
			if err != nil {
				t.Fatalf("LoadSyncLastAt: %v", err)
			}
			if !at.Equal(old) {
				t.Fatalf("sync_last_at = %v, want unchanged %v", at, old)
			}
		})
	}
}

func TestSyncDownloadReplacesLocalState(t *testing.T) {
	engine, db := newSyncEngine(t)
	fake := &fakeDAV{}

	uploadNow := syncTestNow
	svc := newSyncService(engine, db, fake, func() time.Time { return uploadNow })
	mustConfigureSync(t, svc)

	if _, err := engine.Enter(1); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if _, err := engine.Answer(false); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	snapshot := engine.State()
	if _, err := svc.Upload(); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Diverge locally, then let the cloud snapshot win. The first quiz is still
	// active, so another answer records a new attempt.
	if _, err := engine.Answer(true); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if reflect.DeepEqual(engine.State(), snapshot) {
		t.Fatal("local state did not diverge before download")
	}

	downloadNow := syncTestNow.Add(2 * time.Hour)
	svc.now = func() time.Time { return downloadNow }
	res, err := svc.Download()
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got := fake.callLog(); !reflect.DeepEqual(got, []string{"MKCOL", "PUT", "GET"}) {
		t.Fatalf("requests = %v", got)
	}
	if !reflect.DeepEqual(engine.State(), snapshot) {
		t.Fatalf("state after download = %+v, want %+v", engine.State(), snapshot)
	}
	if res.SyncedAt != downloadNow.Format(time.RFC3339) {
		t.Fatalf("synced_at = %q, want download time", res.SyncedAt)
	}
	at, err := db.LoadSyncLastAt()
	if err != nil {
		t.Fatalf("LoadSyncLastAt: %v", err)
	}
	if !at.Equal(downloadNow) {
		t.Fatalf("sync_last_at = %v, want %v", at, downloadNow)
	}
}

func TestSyncDownloadWithoutRemoteFileFails(t *testing.T) {
	engine, db := newSyncEngine(t)
	old := syncTestNow.Add(-time.Hour)
	if err := db.SaveSyncLastAt(old); err != nil {
		t.Fatalf("SaveSyncLastAt: %v", err)
	}
	fake := &fakeDAV{}
	svc := newSyncService(engine, db, fake, fixedNow)
	mustConfigureSync(t, svc)

	before := engine.State()
	_, err := svc.Download()
	if err == nil {
		t.Fatal("Download = nil error, want 404 message")
	}
	if !strings.Contains(err.Error(), "云端还没有同步文件，请先上传") {
		t.Fatalf("error = %q, want the 404 message", err)
	}
	if !reflect.DeepEqual(engine.State(), before) {
		t.Fatal("404 download changed local state")
	}
	at, err := db.LoadSyncLastAt()
	if err != nil {
		t.Fatalf("LoadSyncLastAt: %v", err)
	}
	if !at.Equal(old) {
		t.Fatalf("sync_last_at = %v, want unchanged %v", at, old)
	}
}

func TestSyncDownloadInvalidRemoteKeepsLocalState(t *testing.T) {
	bodies := map[string]string{
		"bad json":          `not json at all`,
		"wrong version":     `{"version":99,"state":{}}`,
		"missing state":     `{"version":1}`,
		"malformed attempt": `{"version":1,"state":{"round":1}}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			engine, db := newSyncEngine(t)
			old := syncTestNow.Add(-time.Hour)
			if err := db.SaveSyncLastAt(old); err != nil {
				t.Fatalf("SaveSyncLastAt: %v", err)
			}
			fake := &fakeDAV{exists: true, data: []byte(body)}
			svc := newSyncService(engine, db, fake, fixedNow)
			mustConfigureSync(t, svc)
			before := engine.State()

			if _, err := svc.Download(); err == nil {
				t.Fatal("Download = nil error, want invalid remote rejection")
			}
			if !reflect.DeepEqual(engine.State(), before) {
				t.Fatal("invalid download changed local state")
			}
			at, err := db.LoadSyncLastAt()
			if err != nil {
				t.Fatalf("LoadSyncLastAt: %v", err)
			}
			if !at.Equal(old) {
				t.Fatalf("sync_last_at = %v, want unchanged %v", at, old)
			}
		})
	}
}

func TestSyncAuthenticationFailureMentionsStatusNotPassword(t *testing.T) {
	engine, db := newSyncEngine(t)
	authErr := errors.New("WebDAV 认证失败（HTTP 401）：请检查用户名与第三方应用密码")

	t.Run("upload", func(t *testing.T) {
		fake := &fakeDAV{putErr: authErr}
		svc := newSyncService(engine, db, fake, fixedNow)
		mustConfigureSync(t, svc)
		_, err := svc.Upload()
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("error = %v, want HTTP 401", err)
		}
		assertNoPassword(t, err)
	})

	t.Run("download", func(t *testing.T) {
		fake := &fakeDAV{getErr: authErr}
		svc := newSyncService(engine, db, fake, fixedNow)
		mustConfigureSync(t, svc)
		_, err := svc.Download()
		if err == nil || !strings.Contains(err.Error(), "401") {
			t.Fatalf("error = %v, want HTTP 401", err)
		}
		assertNoPassword(t, err)
	})
}

func assertNoPassword(t *testing.T, err error) {
	t.Helper()
	if strings.Contains(err.Error(), syncTestPassword) {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestSyncMissingConfigurationSendsNoRequest(t *testing.T) {
	engine, db := newSyncEngine(t)
	fake := &fakeDAV{}
	svc := newSyncService(engine, db, fake, fixedNow)

	for name, call := range map[string]func() error{
		"upload":   func() error { _, err := svc.Upload(); return err },
		"download": func() error { _, err := svc.Download(); return err },
	} {
		err := call()
		if err == nil || !strings.Contains(err.Error(), "请先保存 WebDAV 配置") {
			t.Fatalf("%s error = %v, want missing configuration message", name, err)
		}
	}
	if got := fake.callLog(); len(got) != 0 {
		t.Fatalf("requests = %v, want none without configuration", got)
	}

	// A persisted URL without a password is still unconfigured.
	if _, err := svc.SaveSettings("https://dav.example.com/dav/progress.json", "me@example.com", ""); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if _, err := svc.Upload(); err == nil || !strings.Contains(err.Error(), "请先保存 WebDAV 配置") {
		t.Fatalf("upload without password error = %v", err)
	}
}

func TestSyncSerializesConcurrentCalls(t *testing.T) {
	engine, db := newSyncEngine(t)
	fake := &fakeDAV{delay: 20 * time.Millisecond}
	svc := newSyncService(engine, db, fake, fixedNow)
	mustConfigureSync(t, svc)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, err := svc.Upload(); errs <- err }()
	go func() { defer wg.Done(); _, err := svc.Upload(); errs <- err }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent call: %v", err)
		}
	}
	fake.mu.Lock()
	peak := fake.maxInFlight
	fake.mu.Unlock()
	if peak != 1 {
		t.Fatalf("peak concurrent requests = %d, want 1", peak)
	}
}

func TestSyncTwoDatabases(t *testing.T) {
	fake := &fakeDAV{}
	engineA, dbA := newSyncEngine(t)
	engineB, dbB := newSyncEngine(t)
	svcA := newSyncService(engineA, dbA, fake, fixedNow)
	svcB := newSyncService(engineB, dbB, fake, fixedNow)
	mustConfigureSync(t, svcA)
	mustConfigureSync(t, svcB)

	if _, err := engineA.Enter(1); err != nil {
		t.Fatalf("Enter: %v", err)
	}
	if _, err := engineA.Answer(false); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if _, err := engineA.Answer(true); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	want := engineA.State()

	if _, err := svcA.Upload(); err != nil {
		t.Fatalf("A Upload: %v", err)
	}
	if _, err := svcB.Download(); err != nil {
		t.Fatalf("B Download: %v", err)
	}
	if !reflect.DeepEqual(engineA.State(), engineB.State()) {
		t.Fatalf("states differ after round trip:\nA %+v\nB %+v", engineA.State(), engineB.State())
	}
	if !reflect.DeepEqual(engineB.State(), want) {
		t.Fatalf("B state = %+v, want A's uploaded %+v", engineB.State(), want)
	}
	view, err := svcB.Settings()
	if err != nil {
		t.Fatalf("B Settings: %v", err)
	}
	if view.LastSyncedAt == "" {
		t.Fatal("B last_synced_at is empty after download")
	}
	// The WebDAV configuration is device-local and survives the import.
	if view.URL == "" || !view.PasswordSet {
		t.Fatalf("B lost its WebDAV configuration: %+v", view)
	}
}
