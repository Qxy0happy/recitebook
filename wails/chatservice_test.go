package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"recitebook/internal/chat"
	"recitebook/internal/store"
)

const chatTestKey = "sk-test-secret"

// recordedChatEvent is one emitted Wails event captured by fakeChatEmitter.
type recordedChatEvent struct {
	name string
	data any
}

// fakeChatEmitter stands in for application.Get().Event so tests can assert
// the exact event order and payloads without a running app.
type fakeChatEmitter struct {
	mu     sync.Mutex
	events []recordedChatEvent
}

func (f *fakeChatEmitter) emit(name string, data any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, recordedChatEvent{name: name, data: data})
}

func (f *fakeChatEmitter) snapshot() []recordedChatEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedChatEvent(nil), f.events...)
}

func (f *fakeChatEmitter) names() []string {
	events := f.snapshot()
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.name)
	}
	return names
}

// chatCall is the request body the fake provider captures.
type chatCall struct {
	Model    string         `json:"model"`
	Messages []chat.Message `json:"messages"`
	Stream   bool           `json:"stream"`
}

type chatCallRecorder struct {
	mu    sync.Mutex
	calls []chatCall
}

func (r *chatCallRecorder) add(call chatCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *chatCallRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *chatCallRecorder) at(t *testing.T, index int) chatCall {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if index < 0 || index >= len(r.calls) {
		t.Fatalf("chat call %d missing, have %d", index, len(r.calls))
	}
	return r.calls[index]
}

// openChatService builds the service on a temporary database and swaps in a
// fake emitter.
func openChatService(t *testing.T) (*ChatService, *store.SQLite, *fakeChatEmitter) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	bank, err := NewBankService()
	if err != nil {
		t.Fatalf("NewBankService: %v", err)
	}
	svc := NewChatService(db, bank)
	emitter := &fakeChatEmitter{}
	svc.emit = emitter.emit
	return svc, db, emitter
}

func configureChat(t *testing.T, db *store.SQLite, baseURL string) {
	t.Helper()
	err := db.SaveLLMSettings(store.LLMSettings{
		BaseURL: baseURL + "/v1",
		Model:   "test-model",
		APIKey:  chatTestKey,
	})
	if err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
}

func chatTestQuestion(t *testing.T, svc *ChatService, listNo, index int) (ListDetail, Question) {
	t.Helper()
	detail := svc.bank.Detail(listNo)
	if index >= len(detail.Questions) {
		t.Fatalf("list %d has %d questions, want index %d", listNo, len(detail.Questions), index)
	}
	return detail, detail.Questions[index]
}

// writeChatSSE streams the chunks as OpenAI-compatible SSE data lines.
func writeChatSSE(t *testing.T, w http.ResponseWriter, chunks ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Error("response writer is not a flusher")
		return
	}
	for _, chunk := range chunks {
		payload, err := json.Marshal(map[string]any{
			"choices": []any{
				map[string]any{"delta": map[string]any{"content": chunk}},
			},
		})
		if err != nil {
			t.Errorf("marshal chunk: %v", err)
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func chatTestServer(t *testing.T, recorder *chatCallRecorder, chunks ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call chatCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			t.Errorf("decode chat request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		recorder.add(call)
		writeChatSSE(t, w, chunks...)
	}))
}

// chatEventData decodes an emitted payload the way the frontend receives it.
func chatEventData(t *testing.T, data any) map[string]string {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal event data %s: %v", raw, err)
	}
	return out
}

func chatEventNames(events []recordedChatEvent) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.name)
	}
	return names
}

func TestChatEventsCarryIDs(t *testing.T) {
	svc, db, emitter := openChatService(t)
	recorder := &chatCallRecorder{}
	srv := chatTestServer(t, recorder, "考点是词义辨析，", "注意不可数。")
	defer srv.Close()
	configureChat(t, db, srv.URL)
	detail, question := chatTestQuestion(t, svc, 1, 0)

	reply, err := svc.Ask(AskRequest{
		ListNo:     detail.ListNo,
		QuestionID: question.ID,
		UserAnswer: "intellect",
		Outcome:    "答错",
		Prompt:     "这道题考什么？",
		RequestID:  "req-1",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if want := "考点是词义辨析，注意不可数。"; reply != want {
		t.Fatalf("reply = %q, want %q", reply, want)
	}

	events := emitter.snapshot()
	if got, want := chatEventNames(events), []string{"chat:delta", "chat:delta", "chat:done"}; !equalChatStrings(got, want) {
		t.Fatalf("event names = %v, want %v", got, want)
	}
	var deltas []string
	for _, event := range events {
		payload := chatEventData(t, event.data)
		if payload["request_id"] != "req-1" {
			t.Errorf("%s request_id = %q", event.name, payload["request_id"])
		}
		if payload["question_id"] != question.ID {
			t.Errorf("%s question_id = %q", event.name, payload["question_id"])
		}
		raw, err := json.Marshal(event.data)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		if strings.Contains(string(raw), chatTestKey) {
			t.Errorf("%s payload leaks the API key: %s", event.name, raw)
		}
		if event.name == "chat:delta" {
			deltas = append(deltas, payload["delta"])
		}
		if event.name == "chat:done" && payload["message"] != reply {
			t.Errorf("chat:done message = %q, want %q", payload["message"], reply)
		}
	}
	if want := []string{"考点是词义辨析，", "注意不可数。"}; !equalChatStrings(deltas, want) {
		t.Fatalf("deltas = %v, want %v", deltas, want)
	}

	call := recorder.at(t, 0)
	if call.Model != "test-model" || !call.Stream {
		t.Fatalf("request = %+v, want model and stream:true", call)
	}
	if len(call.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(call.Messages))
	}
	if call.Messages[0].Role != "system" {
		t.Fatalf("messages[0].role = %q", call.Messages[0].Role)
	}
	if want := chatContext(detail, question, "intellect", "答错"); call.Messages[0].Content != want {
		t.Fatalf("system message = %q, want %q", call.Messages[0].Content, want)
	}
	if call.Messages[1].Role != "user" || call.Messages[1].Content != "这道题考什么？" {
		t.Fatalf("last message = %+v", call.Messages[1])
	}
}

func TestChatAskKeepsHistory(t *testing.T) {
	svc, db, _ := openChatService(t)
	recorder := &chatCallRecorder{}
	srv := chatTestServer(t, recorder, "先看词义。")
	defer srv.Close()
	configureChat(t, db, srv.URL)
	detail, question := chatTestQuestion(t, svc, 1, 0)

	base := AskRequest{
		ListNo:     detail.ListNo,
		QuestionID: question.ID,
		UserAnswer: "intellect",
		Outcome:    "答错",
	}
	first := base
	first.Prompt = "第一问"
	first.RequestID = "req-1"
	if _, err := svc.Ask(first); err != nil {
		t.Fatalf("first Ask: %v", err)
	}

	saved, err := db.LoadChat(question.ID)
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	wantSaved := []store.ChatMessage{
		{Role: "user", Content: "第一问"},
		{Role: "assistant", Content: "先看词义。"},
	}
	if !equalChatMessages(saved, wantSaved) {
		t.Fatalf("saved = %+v, want %+v", saved, wantSaved)
	}

	history, err := svc.History(question.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	wantHistory := []ChatMessageView{
		{Role: "user", Content: "第一问"},
		{Role: "assistant", Content: "先看词义。"},
	}
	if !equalChatViews(history, wantHistory) {
		t.Fatalf("History = %+v, want %+v", history, wantHistory)
	}

	empty, err := svc.History("no-such-question")
	if err != nil {
		t.Fatalf("History(empty): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("History without messages = %#v, want empty slice", empty)
	}

	second := base
	second.Prompt = "追问我为什么错"
	second.RequestID = "req-2"
	if _, err := svc.Ask(second); err != nil {
		t.Fatalf("second Ask: %v", err)
	}

	call := recorder.at(t, 1)
	if len(call.Messages) != 4 {
		t.Fatalf("second request messages = %d, want 4", len(call.Messages))
	}
	if want := chatContext(detail, question, "intellect", "答错"); call.Messages[0].Content != want {
		t.Fatalf("system message = %q, want %q", call.Messages[0].Content, want)
	}
	wantTurns := []chat.Message{
		{Role: "user", Content: "第一问"},
		{Role: "assistant", Content: "先看词义。"},
		{Role: "user", Content: "追问我为什么错"},
	}
	if !equalChatTurns(call.Messages[1:], wantTurns) {
		t.Fatalf("second request turns = %+v, want %+v", call.Messages[1:], wantTurns)
	}

	saved, err = db.LoadChat(question.ID)
	if err != nil {
		t.Fatalf("LoadChat after second: %v", err)
	}
	if len(saved) != 4 || saved[3].Role != "assistant" || saved[3].Content != "先看词义。" {
		t.Fatalf("saved after second = %+v", saved)
	}
	for _, message := range saved {
		if strings.Contains(message.Content, chatTestKey) {
			t.Fatalf("stored message leaks the API key: %+v", message)
		}
	}
}

func TestChatAskFailureSavesNothing(t *testing.T) {
	svc, db, emitter := openChatService(t)
	recorder := &chatCallRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call chatCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			t.Errorf("decode chat request: %v", err)
		}
		recorder.add(call)
		http.Error(w, "upstream unavailable", http.StatusInternalServerError)
	}))
	defer srv.Close()
	configureChat(t, db, srv.URL)
	detail, question := chatTestQuestion(t, svc, 1, 0)

	reply, err := svc.Ask(AskRequest{
		ListNo:     detail.ListNo,
		QuestionID: question.ID,
		Prompt:     "第一问",
		RequestID:  "req-fail",
	})
	if err == nil {
		t.Fatal("Ask returned no error")
	}
	if reply != "" {
		t.Fatalf("reply = %q, want empty", reply)
	}
	if !strings.Contains(err.Error(), "接口返回非 200") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), chatTestKey) {
		t.Fatalf("error leaks the API key: %v", err)
	}

	events := emitter.snapshot()
	if got, want := chatEventNames(events), []string{"chat:error"}; !equalChatStrings(got, want) {
		t.Fatalf("event names = %v, want %v", got, want)
	}
	payload := chatEventData(t, events[0].data)
	if payload["error"] != err.Error() {
		t.Fatalf("chat:error error = %q, want %q", payload["error"], err.Error())
	}
	if strings.Contains(payload["error"], chatTestKey) {
		t.Fatalf("event leaks the API key: %q", payload["error"])
	}

	saved, loadErr := db.LoadChat(question.ID)
	if loadErr != nil {
		t.Fatalf("LoadChat: %v", loadErr)
	}
	if len(saved) != 0 {
		t.Fatalf("chats = %+v, want empty", saved)
	}
	if recorder.count() != 1 {
		t.Fatalf("HTTP requests = %d, want 1", recorder.count())
	}
}

func TestChatAskRejectsConcurrentQuestion(t *testing.T) {
	svc, db, _ := openChatService(t)
	recorder := &chatCallRecorder{}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call chatCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			t.Errorf("decode chat request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		recorder.add(call)
		if last := call.Messages[len(call.Messages)-1]; last.Content == "先问" {
			close(entered)
			<-release
		}
		writeChatSSE(t, w, "好")
	}))
	defer srv.Close()
	configureChat(t, db, srv.URL)
	detail, question := chatTestQuestion(t, svc, 1, 0)
	otherDetail, otherQuestion := chatTestQuestion(t, svc, 1, 1)

	type askResult struct {
		reply string
		err   error
	}
	firstDone := make(chan askResult, 1)
	go func() {
		reply, err := svc.Ask(AskRequest{
			ListNo:     detail.ListNo,
			QuestionID: question.ID,
			Prompt:     "先问",
			RequestID:  "req-first",
		})
		firstDone <- askResult{reply: reply, err: err}
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first Ask never reached the provider")
	}

	reply, err := svc.Ask(AskRequest{
		ListNo:     detail.ListNo,
		QuestionID: question.ID,
		Prompt:     "再问一次",
		RequestID:  "req-second",
	})
	if err == nil || !strings.Contains(err.Error(), "该题已有请求在进行") {
		t.Fatalf("second Ask error = %v", err)
	}
	if reply != "" {
		t.Fatalf("second Ask reply = %q, want empty", reply)
	}
	if recorder.count() != 1 {
		t.Fatalf("HTTP requests = %d, want 1 while the first is in flight", recorder.count())
	}

	otherReply, err := svc.Ask(AskRequest{
		ListNo:     otherDetail.ListNo,
		QuestionID: otherQuestion.ID,
		Prompt:     "另一道题",
		RequestID:  "req-other",
	})
	if err != nil {
		t.Fatalf("different question Ask: %v", err)
	}
	if otherReply != "好" {
		t.Fatalf("different question reply = %q, want 好", otherReply)
	}

	unblock()
	select {
	case result := <-firstDone:
		if result.err != nil {
			t.Fatalf("first Ask: %v", result.err)
		}
		if result.reply != "好" {
			t.Fatalf("first reply = %q, want 好", result.reply)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first Ask never finished")
	}
}

func TestChatAskMissingQuestion(t *testing.T) {
	svc, db, emitter := openChatService(t)
	recorder := &chatCallRecorder{}
	srv := chatTestServer(t, recorder, "不会被请求")
	defer srv.Close()
	configureChat(t, db, srv.URL)
	detail, _ := chatTestQuestion(t, svc, 1, 0)

	reply, err := svc.Ask(AskRequest{
		ListNo:     detail.ListNo,
		QuestionID: "L1-999",
		Prompt:     "问一个不存在的题目",
		RequestID:  "req-missing",
	})
	if err == nil || !strings.Contains(err.Error(), "题目不存在") {
		t.Fatalf("error = %v", err)
	}
	if reply != "" {
		t.Fatalf("reply = %q, want empty", reply)
	}
	if recorder.count() != 0 {
		t.Fatalf("HTTP requests = %d, want 0", recorder.count())
	}
	events := emitter.snapshot()
	if got, want := chatEventNames(events), []string{"chat:error"}; !equalChatStrings(got, want) {
		t.Fatalf("event names = %v, want %v", got, want)
	}
	if payload := chatEventData(t, events[0].data); payload["error"] != err.Error() {
		t.Fatalf("chat:error error = %q, want %q", payload["error"], err.Error())
	}
}

func TestChatAskWithoutConfiguration(t *testing.T) {
	svc, db, emitter := openChatService(t)
	recorder := &chatCallRecorder{}
	srv := chatTestServer(t, recorder, "不会被请求")
	defer srv.Close()
	// The base URL is saved but the model and key are not.
	if err := db.SaveLLMSettings(store.LLMSettings{BaseURL: srv.URL + "/v1"}); err != nil {
		t.Fatalf("SaveLLMSettings: %v", err)
	}
	detail, question := chatTestQuestion(t, svc, 1, 0)

	reply, err := svc.Ask(AskRequest{
		ListNo:     detail.ListNo,
		QuestionID: question.ID,
		Prompt:     "第一问",
		RequestID:  "req-unconfigured",
	})
	if !errors.Is(err, chat.ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
	if reply != "" {
		t.Fatalf("reply = %q, want empty", reply)
	}
	if recorder.count() != 0 {
		t.Fatalf("HTTP requests = %d, want 0", recorder.count())
	}
	events := emitter.snapshot()
	if got, want := chatEventNames(events), []string{"chat:error"}; !equalChatStrings(got, want) {
		t.Fatalf("event names = %v, want %v", got, want)
	}
	if payload := chatEventData(t, events[0].data); payload["error"] != chat.ErrNotConfigured.Error() {
		t.Fatalf("chat:error error = %q, want %q", payload["error"], chat.ErrNotConfigured)
	}
}

func TestChatAskRejectsBlankRequest(t *testing.T) {
	svc, _, emitter := openChatService(t)
	cases := []struct {
		name string
		req  AskRequest
	}{
		{"question", AskRequest{QuestionID: " ", Prompt: "问", RequestID: "r"}},
		{"prompt", AskRequest{QuestionID: "L1-01", Prompt: "  ", RequestID: "r"}},
		{"request", AskRequest{QuestionID: "L1-01", Prompt: "问", RequestID: "\t"}},
	}
	for _, tc := range cases {
		reply, err := svc.Ask(tc.req)
		if err == nil {
			t.Errorf("%s: Ask returned no error", tc.name)
		}
		if errors.Is(err, chat.ErrNotConfigured) {
			t.Errorf("%s: validation ran after the settings load", tc.name)
		}
		if reply != "" {
			t.Errorf("%s: reply = %q, want empty", tc.name, reply)
		}
	}
	if events := emitter.snapshot(); len(events) != 0 {
		t.Fatalf("events = %v, want none", chatEventNames(events))
	}
}

// failingChatStore saves nothing and reports a storage failure.
type failingChatStore struct {
	store.ChatStore
}

func (failingChatStore) SaveChat(string, []store.ChatMessage) error {
	return errors.New("磁盘已满")
}

func TestChatSaveFailureStillReturnsReply(t *testing.T) {
	svc, db, emitter := openChatService(t)
	recorder := &chatCallRecorder{}
	srv := chatTestServer(t, recorder, "完整回复")
	defer srv.Close()
	configureChat(t, db, srv.URL)
	detail, question := chatTestQuestion(t, svc, 1, 0)
	svc.chats = failingChatStore{ChatStore: db}

	reply, err := svc.Ask(AskRequest{
		ListNo:     detail.ListNo,
		QuestionID: question.ID,
		Prompt:     "第一问",
		RequestID:  "req-save-fail",
	})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if reply != "完整回复" {
		t.Fatalf("reply = %q, want 完整回复", reply)
	}

	events := emitter.snapshot()
	if got, want := chatEventNames(events), []string{"chat:delta", "chat:done", "chat:error"}; !equalChatStrings(got, want) {
		t.Fatalf("event names = %v, want %v", got, want)
	}
	if payload := chatEventData(t, events[1].data); payload["message"] != reply {
		t.Fatalf("chat:done message = %q, want %q", payload["message"], reply)
	}
	if payload := chatEventData(t, events[2].data); payload["error"] != "回复已显示，但未能保存本地记录" {
		t.Fatalf("chat:error error = %q", payload["error"])
	}

	saved, loadErr := db.LoadChat(question.ID)
	if loadErr != nil {
		t.Fatalf("LoadChat: %v", loadErr)
	}
	if len(saved) != 0 {
		t.Fatalf("chats = %+v, want empty", saved)
	}
}

func equalChatStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalChatMessages(got, want []store.ChatMessage) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			return false
		}
	}
	return true
}

func equalChatTurns(got, want []chat.Message) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			return false
		}
	}
	return true
}

func equalChatViews(got, want []ChatMessageView) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
