package webdav

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const (
	testUser = "me@example.com"
	testPass = "super-secret-app-password"
	testPath = "/dav/背诵 笔记/progress.json"
)

func TestNewRejectsInvalidURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"no scheme", "dav.example.com/dav/progress.json"},
		{"bad scheme", "ftp://dav.example.com/dav/progress.json"},
		{"missing host", "https:///dav/progress.json"},
		{"missing path", "https://dav.example.com"},
		{"root path", "https://dav.example.com/"},
		{"directory path", "https://dav.example.com/dav/recitebook/"},
		{"malformed", "http://[::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateURL(tc.raw); err == nil {
				t.Fatalf("ValidateURL(%q) = nil, want error", tc.raw)
			}
			c := New(Config{URL: tc.raw, Username: testUser, Password: testPass})
			if err := c.Err(); err == nil {
				t.Fatalf("New(%q).Err() = nil, want error", tc.raw)
			}
			if _, _, err := c.Get(context.Background()); err == nil {
				t.Fatalf("Get with invalid URL = nil, want error")
			}
			if err := c.Put(context.Background(), []byte("x")); err == nil {
				t.Fatalf("Put with invalid URL = nil, want error")
			}
			if err := c.EnsureFolder(context.Background()); err == nil {
				t.Fatalf("EnsureFolder with invalid URL = nil, want error")
			}
		})
	}
}

func TestNewAcceptsValidURL(t *testing.T) {
	for _, raw := range []string{
		"https://dav.jianguoyun.com/dav/recitebook/progress.json",
		"http://127.0.0.1:8080/dav/背诵 笔记/progress.json",
		"https://example.com/progress.json?token=abc",
	} {
		if err := ValidateURL(raw); err != nil {
			t.Fatalf("ValidateURL(%q): %v", raw, err)
		}
		if err := New(Config{URL: raw}).Err(); err != nil {
			t.Fatalf("New(%q).Err(): %v", raw, err)
		}
	}
}

func TestGetSuccess(t *testing.T) {
	const body = `{"version":1,"state":{}}`
	var gotMethod, gotPath, gotUserAgent string
	var gotUser, gotPass string
	var gotAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotUserAgent = r.Header.Get("User-Agent")
		gotUser, gotPass, gotAuth = r.BasicAuth()
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL + testPath, Username: testUser, Password: testPass})
	data, exists, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !exists {
		t.Fatal("exists = false, want true")
	}
	if string(data) != body {
		t.Fatalf("body = %q, want %q", data, body)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("method = %q, want GET", gotMethod)
	}
	if gotPath != testPath {
		t.Fatalf("path = %q, want %q", gotPath, testPath)
	}
	if gotUserAgent != "recitebook" {
		t.Fatalf("User-Agent = %q, want recitebook", gotUserAgent)
	}
	if !gotAuth || gotUser != testUser || gotPass != testPass {
		t.Fatalf("basic auth = (%q, %q, %v), want (%q, %q, true)", gotUser, gotPass, gotAuth, testUser, testPass)
	}
}

func TestGetNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	data, exists, err := New(Config{URL: srv.URL + testPath}).Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if exists {
		t.Fatal("exists = true, want false")
	}
	if data != nil {
		t.Fatalf("data = %q, want nil", data)
	}
}

func TestGetAuthErrorHidesPassword(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		c := New(Config{URL: srv.URL + testPath, Username: testUser, Password: testPass})
		_, _, err := c.Get(context.Background())
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: Get err = nil, want error", status)
		}
		if !strings.Contains(err.Error(), "认证") {
			t.Fatalf("status %d: err = %v, want auth wording", status, err)
		}
		if strings.Contains(err.Error(), testPass) {
			t.Fatalf("status %d: err leaks password: %v", status, err)
		}
	}
}

func TestGetStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, _, err := New(Config{URL: srv.URL + testPath}).Get(context.Background())
	if err == nil {
		t.Fatal("Get err = nil, want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want status code 500", err)
	}
}

func TestGetSizeLimit(t *testing.T) {
	t.Run("exactly 8 MiB succeeds", func(t *testing.T) {
		payload := bytes.Repeat([]byte("a"), maxResponseBytes)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		data, exists, err := New(Config{URL: srv.URL + testPath}).Get(context.Background())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !exists || len(data) != maxResponseBytes {
			t.Fatalf("len(data) = %d, exists = %v; want %d, true", len(data), exists, maxResponseBytes)
		}
	})

	t.Run("over 8 MiB fails", func(t *testing.T) {
		payload := bytes.Repeat([]byte("a"), maxResponseBytes+1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		_, _, err := New(Config{URL: srv.URL + testPath}).Get(context.Background())
		if err == nil {
			t.Fatal("Get err = nil, want size error")
		}
		if !strings.Contains(err.Error(), "8 MiB") {
			t.Fatalf("err = %v, want 8 MiB wording", err)
		}
	})
}

func TestPutSuccess(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			const payload = `{"version":1}`
			var gotMethod, gotPath string
			var gotBody []byte
			var gotUser, gotPass string
			var gotAuth bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotUser, gotPass, gotAuth = r.BasicAuth()
				gotBody, _ = io.ReadAll(r.Body)
				w.WriteHeader(status)
			}))
			defer srv.Close()

			c := New(Config{URL: srv.URL + testPath, Username: testUser, Password: testPass})
			if err := c.Put(context.Background(), []byte(payload)); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if gotMethod != http.MethodPut {
				t.Fatalf("method = %q, want PUT", gotMethod)
			}
			if gotPath != testPath {
				t.Fatalf("path = %q, want %q", gotPath, testPath)
			}
			if string(gotBody) != payload {
				t.Fatalf("body = %q, want %q", gotBody, payload)
			}
			if !gotAuth || gotUser != testUser || gotPass != testPass {
				t.Fatalf("basic auth = (%q, %q, %v), want (%q, %q, true)", gotUser, gotPass, gotAuth, testUser, testPass)
			}
		})
	}
}

func TestPutFolderMissing(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		err := New(Config{URL: srv.URL + testPath}).Put(context.Background(), []byte("x"))
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: Put err = nil, want error", status)
		}
		if !strings.Contains(err.Error(), "目录") {
			t.Fatalf("status %d: err = %v, want folder wording", status, err)
		}
	}
}

func TestPutAuthError(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		err := New(Config{URL: srv.URL + testPath, Username: testUser, Password: testPass}).Put(context.Background(), []byte("x"))
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: Put err = nil, want error", status)
		}
		if !strings.Contains(err.Error(), "认证") {
			t.Fatalf("status %d: err = %v, want auth wording", status, err)
		}
	}
}

func TestPutStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := New(Config{URL: srv.URL + testPath}).Put(context.Background(), []byte("x"))
	if err == nil {
		t.Fatal("Put err = nil, want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want status code 500", err)
	}
}

func TestEnsureFolder(t *testing.T) {
	for _, status := range []int{
		http.StatusCreated,
		http.StatusMethodNotAllowed,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var gotMethod, gotPath, gotQuery string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotQuery = r.URL.RawQuery
				w.WriteHeader(status)
			}))
			defer srv.Close()

			c := New(Config{URL: srv.URL + testPath + "?token=abc", Username: testUser, Password: testPass})
			if err := c.EnsureFolder(context.Background()); err != nil {
				t.Fatalf("EnsureFolder: %v", err)
			}
			if gotMethod != "MKCOL" {
				t.Fatalf("method = %q, want MKCOL", gotMethod)
			}
			if want := "/dav/背诵 笔记"; gotPath != want {
				t.Fatalf("path = %q, want %q", gotPath, want)
			}
			if gotQuery != "" {
				t.Fatalf("query = %q, want empty", gotQuery)
			}
		})
	}
}

func TestEnsureFolderConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	err := New(Config{URL: srv.URL + testPath}).EnsureFolder(context.Background())
	if err == nil {
		t.Fatal("EnsureFolder err = nil, want error")
	}
	if !strings.Contains(err.Error(), "网页端") {
		t.Fatalf("err = %v, want wording telling the user to create the folder in the web UI", err)
	}
}

func TestEnsureFolderAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	err := New(Config{URL: srv.URL + testPath, Username: testUser, Password: testPass}).EnsureFolder(context.Background())
	if err == nil {
		t.Fatal("EnsureFolder err = nil, want error")
	}
	if !strings.Contains(err.Error(), "认证") {
		t.Fatalf("err = %v, want auth wording", err)
	}
	if strings.Contains(err.Error(), testPass) {
		t.Fatalf("err leaks password: %v", err)
	}
}

func TestEnsureFolderRejectsUnexpectedStatus(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		err := New(Config{URL: srv.URL + testPath}).EnsureFolder(context.Background())
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: EnsureFolder err = nil, want error", status)
		}
		if !strings.Contains(err.Error(), strconv.Itoa(status)) {
			t.Fatalf("status %d: err = %v, want status code", status, err)
		}
	}
}

func TestEnsureFolderStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := New(Config{URL: srv.URL + testPath}).EnsureFolder(context.Background())
	if err == nil {
		t.Fatal("EnsureFolder err = nil, want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want status code 500", err)
	}
}

func TestNoBasicAuthWithoutUsername(t *testing.T) {
	var gotAuth bool
	var gotUserAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, gotAuth = r.BasicAuth()
		gotUserAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{URL: srv.URL + testPath, Password: testPass})
	if _, _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotAuth {
		t.Fatal("Basic auth header sent with empty username")
	}
	if gotUserAgent != "recitebook" {
		t.Fatalf("User-Agent = %q, want recitebook", gotUserAgent)
	}
}
