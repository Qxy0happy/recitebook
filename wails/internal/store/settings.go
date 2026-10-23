package store

import "strings"

// Additional settings keys. SettingAPIKey is declared in store.go.
const (
	SettingBaseURL = "llm_base_url"
	SettingModel   = "llm_model"
)

// LLMSettings is the OpenAI-compatible grader configuration.
type LLMSettings struct {
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	APIKey  string `json:"api_key"`
}

// SettingsStore reads and writes the LLM settings. It is separate from Store,
// which only exposes recitation state.
type SettingsStore interface {
	LoadLLMSettings() (LLMSettings, error)
	SaveLLMSettings(LLMSettings) error
}

// LoadLLMSettings returns the saved grader configuration.
func (s *SQLite) LoadLLMSettings() (LLMSettings, error) {
	var out LLMSettings
	rows, err := s.db.Query(
		`SELECT key, value FROM settings WHERE key IN (?, ?, ?)`,
		SettingBaseURL, SettingModel, SettingAPIKey,
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
		case SettingBaseURL:
			out.BaseURL = value
		case SettingModel:
			out.Model = value
		case SettingAPIKey:
			out.APIKey = value
		}
	}
	return out, rows.Err()
}

// SaveLLMSettings stores the grader configuration.
func (s *SQLite) SaveLLMSettings(cfg LLMSettings) error {
	tx, err := s.db.Begin()
	if err != nil {
		return classify(err)
	}
	defer tx.Rollback()

	values := []struct {
		key   string
		value string
	}{
		{SettingBaseURL, strings.TrimSpace(cfg.BaseURL)},
		{SettingModel, strings.TrimSpace(cfg.Model)},
		{SettingAPIKey, strings.TrimSpace(cfg.APIKey)},
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
