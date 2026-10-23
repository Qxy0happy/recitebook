package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// WebDAV and sync settings. They describe this device and are never exported.
const (
	SettingWebDAVURL      = "webdav_url"
	SettingWebDAVUsername = "webdav_username"
	SettingWebDAVPassword = "webdav_password"
	SettingSyncLastAt     = "sync_last_at"
)

// WebDAVSettings is the per-device WebDAV sync configuration.
type WebDAVSettings struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// isLocalOnly reports whether key belongs to this device alone: secrets and
// sync bookkeeping stay out of exports and survive imports.
func isLocalOnly(key string) bool {
	return key == SettingAPIKey ||
		strings.HasPrefix(key, "webdav_") ||
		strings.HasPrefix(key, "sync_")
}

// LoadWebDAVSettings returns the saved WebDAV configuration.
func (s *SQLite) LoadWebDAVSettings() (WebDAVSettings, error) {
	var out WebDAVSettings
	rows, err := s.db.Query(
		`SELECT key, value FROM settings WHERE key IN (?, ?, ?)`,
		SettingWebDAVURL, SettingWebDAVUsername, SettingWebDAVPassword,
	)
	if err != nil {
		return out, classify(err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return out, classify(err)
		}
		switch key {
		case SettingWebDAVURL:
			out.URL = value
		case SettingWebDAVUsername:
			out.Username = value
		case SettingWebDAVPassword:
			out.Password = value
		}
	}
	return out, rows.Err()
}

// SaveWebDAVSettings stores the WebDAV configuration. The password is kept
// verbatim; only the URL and user name are trimmed.
func (s *SQLite) SaveWebDAVSettings(cfg WebDAVSettings) error {
	tx, err := s.db.Begin()
	if err != nil {
		return classify(err)
	}
	defer tx.Rollback()

	values := []struct {
		key   string
		value string
	}{
		{SettingWebDAVURL, strings.TrimSpace(cfg.URL)},
		{SettingWebDAVUsername, strings.TrimSpace(cfg.Username)},
		{SettingWebDAVPassword, cfg.Password},
	}
	for _, item := range values {
		if _, err := tx.Exec(
			`INSERT INTO settings (key, value) VALUES (?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
			item.key, item.value,
		); err != nil {
			return classify(err)
		}
	}
	return classify(tx.Commit())
}

// LoadSyncLastAt returns the last successful sync time, or the zero time when
// the device has never synced.
func (s *SQLite) LoadSyncLastAt() (time.Time, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, SettingSyncLastAt).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, classify(err)
	}
	if raw == "" {
		return time.Time{}, nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s: %w", SettingSyncLastAt, err)
	}
	return at, nil
}

// SaveSyncLastAt records the last successful sync time in RFC3339.
func (s *SQLite) SaveSyncLastAt(at time.Time) error {
	value := ""
	if !at.IsZero() {
		value = at.Format(time.RFC3339)
	}
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		SettingSyncLastAt, value,
	)
	return classify(err)
}

// loadLocalSettings reads the device-local settings inside tx so an import can
// restore them after replacing everything else.
func loadLocalSettings(tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.Query(`SELECT key, value FROM settings`)
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
			out[key] = value
		}
	}
	return out, rows.Err()
}
