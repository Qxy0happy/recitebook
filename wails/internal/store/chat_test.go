package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestChatRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	want := []ChatMessage{
		{Role: "user", Content: "explain the past perfect"},
		{Role: "assistant", Content: "it marks an action before another past action"},
	}
	if err := s.SaveChat("q1", want); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}

	got, err := s.LoadChat("q1")
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("LoadChat returned %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestChatSaveReplaces(t *testing.T) {
	s, _ := openTemp(t)
	first := []ChatMessage{
		{Role: "user", Content: "first question"},
		{Role: "assistant", Content: "first answer"},
	}
	if err := s.SaveChat("q1", first); err != nil {
		t.Fatalf("SaveChat first: %v", err)
	}
	second := []ChatMessage{
		{Role: "user", Content: "second question"},
	}
	if err := s.SaveChat("q1", second); err != nil {
		t.Fatalf("SaveChat second: %v", err)
	}

	got, err := s.LoadChat("q1")
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	if len(got) != 1 || got[0] != second[0] {
		t.Fatalf("LoadChat = %+v, want exactly %+v", got, second)
	}
}

func TestChatMissingReturnsEmpty(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.LoadChat("absent")
	if err != nil {
		t.Fatalf("LoadChat: %v", err)
	}
	if got == nil {
		t.Fatal("LoadChat returned a nil slice, want an empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("LoadChat returned %+v, want empty", got)
	}
}

func TestChatRejectsEmptyID(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.LoadChat(""); !errors.Is(err, ErrEmptyChatID) {
		t.Fatalf("LoadChat error = %v, want ErrEmptyChatID", err)
	}
	if err := s.SaveChat("", []ChatMessage{{Role: "user", Content: "hi"}}); !errors.Is(err, ErrEmptyChatID) {
		t.Fatalf("SaveChat error = %v, want ErrEmptyChatID", err)
	}
}

func TestChatNotExported(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.Save(sampleState()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.SaveChat("q1", []ChatMessage{{Role: "user", Content: "secret chat"}}); err != nil {
		t.Fatalf("SaveChat: %v", err)
	}
	var buf bytes.Buffer
	if err := s.Export(&buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte("chats")) || bytes.Contains(buf.Bytes(), []byte("secret chat")) {
		t.Fatalf("Export leaked chat data: %s", buf.String())
	}
	var generic map[string]any
	if err := json.Unmarshal(buf.Bytes(), &generic); err != nil {
		t.Fatalf("Unmarshal export: %v", err)
	}
	if _, ok := generic["chats"]; ok {
		t.Fatal("Export contains a chats key, want none")
	}
}
