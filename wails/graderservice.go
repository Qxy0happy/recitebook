package main

import (
	"context"
	"strings"
	"sync"

	"recitebook/internal/grader"
	"recitebook/internal/store"
)

// GradeResult is one grading outcome shown after a translation answer.
type GradeResult struct {
	Score   int      `json:"score"`
	Issues  []string `json:"issues"`
	Comment string   `json:"comment"`
	Correct bool     `json:"correct"`
}

// LLMSettingsView is the settings screen projection. The API key never leaves
// the backend; only whether one is saved is reported.
type LLMSettingsView struct {
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	APIKeySet bool   `json:"api_key_set"`
}

// GradeService grades translation answers and owns the LLM configuration.
type GradeService struct {
	mu     sync.Mutex
	store  store.SettingsStore
	grader *grader.Client
}

// NewGradeService builds the grading service on top of the settings store.
func NewGradeService(db store.SettingsStore) *GradeService {
	return &GradeService{store: db, grader: grader.New(nil)}
}

// Settings returns the saved configuration with the key masked.
func (s *GradeService) Settings() (LLMSettingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg, err := s.store.LoadLLMSettings()
	if err != nil {
		return LLMSettingsView{}, err
	}
	return view(cfg), nil
}

// SaveSettings stores the configuration. An empty APIKey keeps the saved key.
func (s *GradeService) SaveSettings(baseURL, model, apiKey string) (LLMSettingsView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.store.LoadLLMSettings()
	if err != nil {
		return LLMSettingsView{}, err
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		key = current.APIKey
	}
	next := store.LLMSettings{
		BaseURL: strings.TrimSpace(baseURL),
		Model:   strings.TrimSpace(model),
		APIKey:  key,
	}
	if err := s.store.SaveLLMSettings(next); err != nil {
		return LLMSettingsView{}, err
	}
	return view(next), nil
}

// Grade scores one full-sentence translation. Errors are safe to surface:
// they never contain the API key.
func (s *GradeService) Grade(questionID, stem, answer, reference string) (GradeResult, error) {
	cfg, err := s.store.LoadLLMSettings()
	if err != nil {
		return GradeResult{}, err
	}
	result, err := s.grader.Grade(context.Background(), grader.Config{
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		APIKey:  cfg.APIKey,
	}, grader.Submission{
		QuestionID: questionID,
		Stem:       stem,
		Answer:     answer,
		Reference:  reference,
	})
	if err != nil {
		return GradeResult{}, err
	}
	return GradeResult{
		Score:   result.Score,
		Issues:  result.Issues,
		Comment: result.Comment,
		Correct: result.Correct(),
	}, nil
}

func view(cfg store.LLMSettings) LLMSettingsView {
	return LLMSettingsView{
		BaseURL:   cfg.BaseURL,
		Model:     cfg.Model,
		APIKeySet: cfg.APIKey != "",
	}
}
