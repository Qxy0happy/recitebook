package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"recitebook/internal/chat"
	"recitebook/internal/store"
)

const (
	// chatHistoryWindow is how many stored turns go back to the model. The
	// full history still lives in SQLite.
	chatHistoryWindow = 20
	// chatRequestTimeout matches the streaming client's own timeout; dialogue
	// needs longer than grading.
	chatRequestTimeout = 120 * time.Second

	chatEventDelta = "chat:delta"
	chatEventDone  = "chat:done"
	chatEventError = "chat:error"
)

var (
	errChatIncomplete      = errors.New("chat: 请求参数不完整")
	errChatInflight        = errors.New("chat: 该题已有请求在进行")
	errChatQuestionMissing = errors.New("chat: 题目不存在")
	errChatSaveFailed      = errors.New("回复已显示，但未能保存本地记录")
)

// chatSystemPrompt is the fixed tutor instruction that precedes the rebuilt
// question context in every system message.
const chatSystemPrompt = `你是考研英语词汇辅导老师。只讲本题与所给词卡能支持的内容；
需要补充课外知识时先说明。先用一句话点明考点，再解释正确答案的理由，
最后指出学生答案的问题和改法。不要编造书中未出现的释义、搭配或真题。
回答用中文，术语保留英文原文。`

// ChatMessageView is one stored turn handed to the conversation panel.
type ChatMessageView struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// AskRequest is one question asked about a single bank item. The backend owns
// the configuration, context, single-flight and persistence.
type AskRequest struct {
	ListNo     int    `json:"list_no"`
	QuestionID string `json:"question_id"`
	UserAnswer string `json:"user_answer"`
	Outcome    string `json:"outcome"`
	Prompt     string `json:"prompt"`
	RequestID  string `json:"request_id"`
}

type chatDeltaEvent struct {
	RequestID  string `json:"request_id"`
	QuestionID string `json:"question_id"`
	Delta      string `json:"delta"`
}

type chatDoneEvent struct {
	RequestID  string `json:"request_id"`
	QuestionID string `json:"question_id"`
	Message    string `json:"message"`
}

type chatErrorEvent struct {
	RequestID  string `json:"request_id"`
	QuestionID string `json:"question_id"`
	Error      string `json:"error"`
}

// ChatService streams answers to questions about bank items and keeps the
// per-question conversation in SQLite. The API key is read from settings and
// never leaves this type: it is not stored, emitted, or wrapped into errors.
type ChatService struct {
	mu       sync.Mutex
	settings store.SettingsStore
	chats    store.ChatStore
	bank     *BankService
	client   *chat.Client
	emit     func(name string, data any)
	inflight map[string]bool
}

// NewChatService builds the chat service on the shared database and bank.
func NewChatService(db *store.SQLite, bank *BankService) *ChatService {
	return &ChatService{
		settings: db,
		chats:    db,
		bank:     bank,
		client:   chat.New(nil),
		emit:     wailsChatEmitter,
		inflight: map[string]bool{},
	}
}

// wailsChatEmitter publishes one event to the frontend. It is a no-op before
// the application exists, which keeps tests free of a running app.
func wailsChatEmitter(name string, data any) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit(name, data)
}

// History returns the saved conversation for a question. An unknown question
// yields an empty slice, not an error.
func (s *ChatService) History(questionID string) ([]ChatMessageView, error) {
	messages, err := s.chats.LoadChat(questionID)
	if err != nil {
		return nil, err
	}
	out := make([]ChatMessageView, 0, len(messages))
	for _, message := range messages {
		out = append(out, ChatMessageView{Role: message.Role, Content: message.Content})
	}
	return out, nil
}

// Ask streams one answer about a question. Deltas and the final reply are
// pushed as Wails events; a successful exchange is appended to the question's
// history. Failures emit chat:error and store nothing.
func (s *ChatService) Ask(req AskRequest) (string, error) {
	questionID := strings.TrimSpace(req.QuestionID)
	prompt := strings.TrimSpace(req.Prompt)
	requestID := strings.TrimSpace(req.RequestID)
	if questionID == "" || prompt == "" || requestID == "" {
		return "", errChatIncomplete
	}

	if !s.begin(questionID) {
		return "", errChatInflight
	}
	defer s.end(questionID)

	cfg, err := s.settings.LoadLLMSettings()
	if err != nil {
		s.emit(chatEventError, chatErrorEvent{RequestID: requestID, QuestionID: questionID, Error: err.Error()})
		return "", err
	}
	provider := chat.Config{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey}
	if !provider.Complete() {
		s.emit(chatEventError, chatErrorEvent{RequestID: requestID, QuestionID: questionID, Error: chat.ErrNotConfigured.Error()})
		return "", chat.ErrNotConfigured
	}

	detail := s.bank.Detail(req.ListNo)
	question, ok := findChatQuestion(detail, questionID)
	if !ok {
		s.emit(chatEventError, chatErrorEvent{RequestID: requestID, QuestionID: questionID, Error: errChatQuestionMissing.Error()})
		return "", errChatQuestionMissing
	}

	history, err := s.chats.LoadChat(questionID)
	if err != nil {
		s.emit(chatEventError, chatErrorEvent{RequestID: requestID, QuestionID: questionID, Error: err.Error()})
		return "", err
	}

	messages := make([]chat.Message, 0, chatHistoryWindow+2)
	messages = append(messages, chat.Message{
		Role:    "system",
		Content: chatSystemPrompt + "\n\n" + chatContext(detail, question, req.UserAnswer, req.Outcome),
	})
	recent := history
	if len(recent) > chatHistoryWindow {
		recent = recent[len(recent)-chatHistoryWindow:]
	}
	for _, message := range recent {
		messages = append(messages, chat.Message{Role: message.Role, Content: message.Content})
	}
	messages = append(messages, chat.Message{Role: "user", Content: prompt})

	ctx, cancel := context.WithTimeout(context.Background(), chatRequestTimeout)
	defer cancel()
	reply, err := s.client.Stream(ctx, provider, messages, func(delta string) {
		s.emit(chatEventDelta, chatDeltaEvent{RequestID: requestID, QuestionID: questionID, Delta: delta})
	})
	if err != nil {
		s.emit(chatEventError, chatErrorEvent{RequestID: requestID, QuestionID: questionID, Error: err.Error()})
		return "", err
	}

	saved := append(history, store.ChatMessage{Role: "user", Content: prompt})
	saved = append(saved, store.ChatMessage{Role: "assistant", Content: reply})
	if err := s.chats.SaveChat(questionID, saved); err != nil {
		// The reply is already on screen; the failure only costs the record.
		s.emit(chatEventDone, chatDoneEvent{RequestID: requestID, QuestionID: questionID, Message: reply})
		s.emit(chatEventError, chatErrorEvent{RequestID: requestID, QuestionID: questionID, Error: errChatSaveFailed.Error()})
		return reply, nil
	}
	s.emit(chatEventDone, chatDoneEvent{RequestID: requestID, QuestionID: questionID, Message: reply})
	return reply, nil
}

// begin claims the single in-flight slot for a question.
func (s *ChatService) begin(questionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight[questionID] {
		return false
	}
	s.inflight[questionID] = true
	return true
}

// end releases the in-flight slot claimed by begin.
func (s *ChatService) end(questionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.inflight, questionID)
}

// findChatQuestion looks the question up in the list the frontend named.
func findChatQuestion(detail ListDetail, questionID string) (Question, bool) {
	for _, question := range detail.Questions {
		if question.ID == questionID {
			return question, true
		}
	}
	return Question{}, false
}
