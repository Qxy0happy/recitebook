package grader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func reply(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	body, err := json.Marshal(map[string]any{
		"choices": []any{
			map[string]any{"message": map[string]any{"content": content}},
		},
	})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	_, _ = w.Write(body)
}

func testConfig(url string) Config {
	return Config{BaseURL: url + "/v1", Model: "test-model", APIKey: "sk-test"}
}

func testSubmission() Submission {
	return Submission{
		QuestionID: "L1-09",
		Stem:       "Companies rely on artificial intelligence.",
		Reference:  "公司依赖人工智能。",
		Answer:     "公司依靠人工智能。",
	}
}

func TestGradeSuccess(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q", got)
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if body.Model != "test-model" {
			t.Errorf("model = %q", body.Model)
		}
		joined := ""
		for _, message := range body.Messages {
			joined += message.Content
		}
		if !strings.Contains(joined, "公司依靠人工智能。") {
			t.Errorf("request does not carry the answer: %q", joined)
		}
		reply(t, w, `{"score": 85, "issues": ["时态不一致"], "comment": "基本达意"}`)
	}))
	defer srv.Close()

	client := New(nil)
	got, err := client.Grade(context.Background(), testConfig(srv.URL), testSubmission())
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if got.Score != 85 || len(got.Issues) != 1 || got.Issues[0] != "时态不一致" || got.Comment != "基本达意" {
		t.Fatalf("result = %+v", got)
	}
	if !got.Correct() {
		t.Fatalf("85 should be correct")
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestGradeThreshold(t *testing.T) {
	tests := []struct {
		name    string
		score   int
		correct bool
	}{
		{name: "perfect", score: 100, correct: true},
		{name: "at threshold", score: 80, correct: true},
		{name: "below threshold", score: 79, correct: false},
		{name: "zero", score: 0, correct: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reply(t, w, `{"score": `+itoa(tc.score)+`, "issues": [], "comment": "ok"}`)
			}))
			defer srv.Close()

			got, err := New(nil).Grade(context.Background(), testConfig(srv.URL), testSubmission())
			if err != nil {
				t.Fatalf("Grade: %v", err)
			}
			if got.Correct() != tc.correct {
				t.Fatalf("score %d Correct() = %v, want %v", tc.score, got.Correct(), tc.correct)
			}
		})
	}
}

func TestGradeFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			name: "non 200",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "provider said no sk-test", http.StatusUnauthorized)
			},
			want: ErrStatus,
		},
		{
			name: "invalid envelope",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("not json"))
			},
			want: ErrInvalidReply,
		},
		{
			name: "invalid content",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				reply(t, w, "评分：85 分")
			},
			want: ErrInvalidReply,
		},
		{
			name: "empty choices",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"choices": []}`))
			},
			want: ErrInvalidReply,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			_, err := New(nil).Grade(context.Background(), testConfig(srv.URL), testSubmission())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), "sk-test") {
				t.Fatalf("error leaks the API key: %v", err)
			}
		})
	}
}

func TestGradeTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		reply(t, w, `{"score": 90}`)
	}))
	defer srv.Close()

	client := New(&http.Client{Timeout: 60 * time.Millisecond})
	_, err := client.Grade(context.Background(), testConfig(srv.URL), testSubmission())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
}

func TestGradeWithoutConfiguration(t *testing.T) {
	_, err := New(nil).Grade(context.Background(), Config{}, testSubmission())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("error = %v, want ErrNotConfigured", err)
	}
}

func TestGradeCacheHit(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&requests, 1)
		reply(t, w, `{"score": 90, "issues": [], "comment": "不错"}`)
	}))
	defer srv.Close()

	client := New(nil)
	cfg := testConfig(srv.URL)
	first, err := client.Grade(context.Background(), cfg, testSubmission())
	if err != nil {
		t.Fatalf("first Grade: %v", err)
	}
	repeat := testSubmission()
	repeat.Answer = "  公司依靠人工智能。  "
	second, err := client.Grade(context.Background(), cfg, repeat)
	if err != nil {
		t.Fatalf("second Grade: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached result differs: first %+v second %+v", first, second)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1 (cache hit must not send)", requests)
	}

	other := testSubmission()
	other.Answer = "公司依赖人工智能。"
	if _, err := client.Grade(context.Background(), cfg, other); err != nil {
		t.Fatalf("third Grade: %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2 (different answer must send)", requests)
	}
}

func TestGradeFailuresAreNotCached(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		reply(t, w, `{"score": 88, "issues": [], "comment": "ok"}`)
	}))
	defer srv.Close()

	client := New(nil)
	cfg := testConfig(srv.URL)
	if _, err := client.Grade(context.Background(), cfg, testSubmission()); !errors.Is(err, ErrStatus) {
		t.Fatalf("first error = %v, want ErrStatus", err)
	}
	got, err := client.Grade(context.Background(), cfg, testSubmission())
	if err != nil {
		t.Fatalf("retry Grade: %v", err)
	}
	if got.Score != 88 || requests != 2 {
		t.Fatalf("retry result = %+v requests = %d", got, requests)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}
