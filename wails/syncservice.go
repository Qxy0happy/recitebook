package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"recitebook/internal/store"
	"recitebook/internal/webdav"
)

// WebDAVSettingsView is the frontend view of the per-device sync settings.
// The password is never returned; PasswordSet only reports whether one exists.
type WebDAVSettingsView struct {
	URL          string `json:"url"`
	Username     string `json:"username"`
	PasswordSet  bool   `json:"password_set"`
	LastSyncedAt string `json:"last_synced_at"`
}

// SyncResult reports when a successful upload or download finished.
type SyncResult struct {
	SyncedAt string `json:"synced_at"`
}

// davClient is the small WebDAV surface the sync service needs. Tests replace
// it with an in-memory fake through SyncService.newClient.
type davClient interface {
	Get(context.Context) ([]byte, bool, error)
	Put(context.Context, []byte) error
	EnsureFolder(context.Context) error
}

// syncStore narrows the database to the settings the sync service owns.
type syncStore interface {
	LoadWebDAVSettings() (store.WebDAVSettings, error)
	SaveWebDAVSettings(store.WebDAVSettings) error
	LoadSyncLastAt() (time.Time, error)
	SaveSyncLastAt(time.Time) error
}

var (
	errSyncNotConfigured = errors.New("请先保存 WebDAV 配置")
	errRemoteMissing     = errors.New("云端还没有同步文件，请先上传")
)

// SyncService moves the progress snapshot between this device and the user's
// WebDAV file. Upload and download are explicit, one-way, last-writer-wins
// operations: the user picks the direction, nothing is merged.
type SyncService struct {
	mu  sync.Mutex
	eng *EngineService
	db  syncStore
	now func() time.Time
	// newClient builds a client from the saved configuration. It is a field so
	// tests can inject an in-memory server without HTTP.
	newClient func(store.WebDAVSettings) (davClient, error)
}

// NewSyncService builds the sync service on top of the engine and database.
func NewSyncService(engine *EngineService, db *store.SQLite) *SyncService {
	return &SyncService{
		eng:       engine,
		db:        db,
		now:       time.Now,
		newClient: webdavClient,
	}
}

// webdavClient is the production client factory.
func webdavClient(cfg store.WebDAVSettings) (davClient, error) {
	client := webdav.New(webdav.Config{
		URL:      cfg.URL,
		Username: cfg.Username,
		Password: cfg.Password,
	})
	if err := client.Err(); err != nil {
		return nil, err
	}
	return client, nil
}

// Settings returns the saved configuration without the password.
func (s *SyncService) Settings() (WebDAVSettingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settingsLocked()
}

func (s *SyncService) settingsLocked() (WebDAVSettingsView, error) {
	cfg, err := s.db.LoadWebDAVSettings()
	if err != nil {
		return WebDAVSettingsView{}, fmt.Errorf("读取 WebDAV 配置失败：%w", err)
	}
	view := WebDAVSettingsView{
		URL:         cfg.URL,
		Username:    cfg.Username,
		PasswordSet: cfg.Password != "",
	}
	at, err := s.db.LoadSyncLastAt()
	if err != nil {
		return WebDAVSettingsView{}, fmt.Errorf("读取上次同步时间失败：%w", err)
	}
	if !at.IsZero() {
		view.LastSyncedAt = at.Format(time.RFC3339)
	}
	return view, nil
}

// SaveSettings validates and stores the configuration. An empty password keeps
// the stored one, so the frontend never has to echo the secret back.
func (s *SyncService) SaveSettings(url, username, password string) (WebDAVSettingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := webdav.ValidateURL(url); err != nil {
		return WebDAVSettingsView{}, err
	}
	current, err := s.db.LoadWebDAVSettings()
	if err != nil {
		return WebDAVSettingsView{}, fmt.Errorf("读取 WebDAV 配置失败：%w", err)
	}
	if password == "" {
		password = current.Password
	}
	if err := s.db.SaveWebDAVSettings(store.WebDAVSettings{
		URL:      strings.TrimSpace(url),
		Username: strings.TrimSpace(username),
		Password: password,
	}); err != nil {
		return WebDAVSettingsView{}, fmt.Errorf("保存 WebDAV 配置失败：%w", err)
	}
	return s.settingsLocked()
}

// Upload overwrites the cloud file with this device's progress. The engine lock
// is held only while exporting, never during the network calls.
func (s *SyncService) Upload() (SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, err := s.clientLocked()
	if err != nil {
		return SyncResult{}, err
	}

	var buf bytes.Buffer
	if err := s.eng.exportTo(&buf); err != nil {
		return SyncResult{}, fmt.Errorf("读取本机进度失败：%w", err)
	}

	ctx := context.Background()
	if err := client.EnsureFolder(ctx); err != nil {
		return SyncResult{}, err
	}
	if err := client.Put(ctx, buf.Bytes()); err != nil {
		return SyncResult{}, err
	}
	return s.recordSync()
}

// Download overwrites this device's progress with the cloud file. A missing or
// invalid remote file leaves both the local state and sync_last_at untouched.
func (s *SyncService) Download() (SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	client, err := s.clientLocked()
	if err != nil {
		return SyncResult{}, err
	}

	data, exists, err := client.Get(context.Background())
	if err != nil {
		return SyncResult{}, err
	}
	if !exists {
		return SyncResult{}, errRemoteMissing
	}
	if _, err := s.eng.importFrom(bytes.NewReader(data)); err != nil {
		return SyncResult{}, fmt.Errorf("云端文件格式不正确，本机进度未改动：%w", err)
	}
	return s.recordSync()
}

// clientLocked checks that the device is configured and builds a client.
func (s *SyncService) clientLocked() (davClient, error) {
	cfg, err := s.db.LoadWebDAVSettings()
	if err != nil {
		return nil, fmt.Errorf("读取 WebDAV 配置失败：%w", err)
	}
	if strings.TrimSpace(cfg.URL) == "" || cfg.Password == "" {
		return nil, errSyncNotConfigured
	}
	return s.newClient(cfg)
}

// recordSync stamps the successful operation and reports it to the caller.
func (s *SyncService) recordSync() (SyncResult, error) {
	at := s.now()
	if err := s.db.SaveSyncLastAt(at); err != nil {
		return SyncResult{}, fmt.Errorf("保存同步时间失败：%w", err)
	}
	return SyncResult{SyncedAt: at.Format(time.RFC3339)}, nil
}
