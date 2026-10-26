package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(url string) Config {
	return Config{BaseURL: url + "/v1", Model: "test-model", APIKey: "sk-test"}
}

func testMessages() []Message {
	return []Message{
		{Role: "system", Content: "你是考研英语助教。"},
		{Role: "user", Content: "这道题考什么？"},
	}
}

// sseChunk writes one content delta as a data line.
func sseChunk(w http.ResponseWriter, content string) {
	payload, err := json.Marshal(map[string]any{
		"choices": []any{
			map[string]any{"delta": map[string]any{"content": content}},
		},
	})
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, "data: %s\n\n", payload)
}

func TestStreamSuccess(t *testing.T) {
	var requests int32
	var path, auth string
	var body struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		Stream      bool      `json:"stream"`
		Temperature float64   `json:"temperature"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type = %q, want application/json", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer is not a flusher")
			return
		}
		for _, chunk := range []string{"这道题考", "的是虚拟语气，", "注意与过去事实相反。"} {
			sseChunk(w, chunk)
			flusher.Flush()
		}
		fmt.Fprint(w, ": keep-alive comment\n\n")
		fmt.Fprint(w, "event: message\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	var deltas []string
	got, err := New(nil).Stream(context.Background(), testConfig(srv.URL), testMessages(), func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	want := "这道题考的是虚拟语气，注意与过去事实相反。"
	if got != want {
		t.Fatalf("reply = %q, want %q", got, want)
	}
	if len(deltas) != 3 || strings.Join(deltas, "") != want {
		t.Fatalf("deltas = %q, want the three chunks in order", deltas)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if path != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", path)
	}
	if auth != "Bearer sk-test" {
		t.Fatalf("authorization = %q", auth)
	}
	if body.Stream != true {
		t.Fatalf("stream = %v, want true", body.Stream)
	}
	if body.Temperature != 0.7 {
		t.Fatalf("temperature = %v, want 0.7", body.Temperature)
	}
	if body.Model != "test-model" {
		t.Fatalf("model = %q", body.Model)
	}
	if !reflect.DeepEqual(body.Messages, testMessages()) {
		t.Fatalf("messages = %+v, want %+v", body.Messages, testMessages())
	}
}

func TestStreamStatusError(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		http.Error(w, strings.Repeat("upstream exploded; ", 4000), http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := New(nil).Stream(context.Background(), testConfig(srv.URL), testMessages(), nil)
	if !errors.Is(err, ErrStatus) {
		t.Fatalf("error = %v, want ErrStatus", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestStreamInvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: not-json\n\n")
	}))
	defer srv.Close()

	_, err := New(nil).Stream(context.Background(), testConfig(srv.URL), testMessages(), nil)
	if !errors.Is(err, ErrInvalidReply) {
		t.Fatalf("error = %v, want ErrInvalidReply", err)
	}
}

func TestStreamEmptyReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	onDelta := 0
	got, err := New(nil).Stream(context.Background(), testConfig(srv.URL), testMessages(), func(string) { onDelta++ })
	if !errors.Is(err, ErrEmptyReply) {
		t.Fatalf("error = %v, want ErrEmptyReply", err)
	}
	if got != "" {
		t.Fatalf("reply = %q, want empty", got)
	}
	if onDelta != 0 {
		t.Fatalf("onDelta calls = %d, want 0", onDelta)
	}
}

func TestStreamNotConfigured(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
	}))
	defer srv.Close()

	cases := map[string]Config{
		"no base url":  {Model: "test-model", APIKey: "sk-test"},
		"blank base":   {BaseURL: "   ", Model: "test-model", APIKey: "sk-test"},
		"no model":     {BaseURL: srv.URL, APIKey: "sk-test"},
		"no api key":   {BaseURL: srv.URL, Model: "test-model"},
		"empty config": {},
	}
	for name, cfg := range cases {
		if _, err := New(nil).Stream(context.Background(), cfg, testMessages(), nil); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("%s: error = %v, want ErrNotConfigured", name, err)
		}
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestStreamTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		sseChunk(w, "迟到的内容")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := New(nil).Stream(ctx, testConfig(srv.URL), testMessages(), nil)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
}

func TestNewDefaultTimeout(t *testing.T) {
	if got := New(nil).http.Timeout; got != 120*time.Second {
		t.Fatalf("default timeout = %v, want 120s", got)
	}
	custom := &http.Client{Timeout: 3 * time.Second}
	if got := New(custom).http; got != custom {
		t.Fatal("New did not use the provided http.Client")
	}
}
