// Package chat streams replies from an OpenAI-compatible chat completion
// endpoint, one delta at a time.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrNotConfigured means the base URL, model or API key is missing.
	ErrNotConfigured = errors.New("chat: 未配置可用的模型与密钥")
	// ErrTimeout means the provider did not answer in time.
	ErrTimeout = errors.New("chat: 请求超时")
	// ErrStatus means the provider answered with a non-200 status.
	ErrStatus = errors.New("chat: 接口返回非 200")
	// ErrInvalidReply means a streamed chunk cannot be read as a chat chunk.
	ErrInvalidReply = errors.New("chat: 返回内容无法解析")
	// ErrEmptyReply means the stream ended without any content.
	ErrEmptyReply = errors.New("chat: 模型未返回内容")
)

const (
	defaultTimeout = 120 * time.Second
	maxEventSize   = 1 << 20
)

// Config is the OpenAI-compatible provider configuration.
type Config struct {
	BaseURL string
	Model   string
	APIKey  string
}

// Complete reports whether every field needed for a request is present.
func (c Config) Complete() bool {
	return strings.TrimSpace(c.BaseURL) != "" &&
		strings.TrimSpace(c.Model) != "" &&
		strings.TrimSpace(c.APIKey) != ""
}

// Message is one turn of the conversation sent to the model.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Client streams chat completions.
type Client struct {
	http *http.Client
}

// New builds a client. A nil http.Client gets a 120 second timeout.
func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{http: httpClient}
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature"`
}

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// Stream sends the conversation and returns the complete reply. onDelta is
// called with every non-empty content chunk as it arrives; a nil onDelta is
// ignored. Messages and the reply body are never logged.
func (c *Client) Stream(ctx context.Context, cfg Config, messages []Message, onDelta func(string)) (string, error) {
	if !cfg.Complete() {
		return "", ErrNotConfigured
	}

	payload, err := json.Marshal(chatRequest{
		Model:       cfg.Model,
		Messages:    messages,
		Stream:      true,
		Temperature: 0.7,
	})
	if err != nil {
		return "", fmt.Errorf("chat: 构造请求失败: %w", err)
	}

	url := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("chat: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		if isTimeout(err) {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("chat: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return "", fmt.Errorf("%w: %d", ErrStatus, resp.StatusCode)
	}

	var reply strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), maxEventSize)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidReply, err)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		content := chunk.Choices[0].Delta.Content
		if content == "" {
			continue
		}
		reply.WriteString(content)
		if onDelta != nil {
			onDelta(content)
		}
	}
	if err := scanner.Err(); err != nil {
		if isTimeout(err) {
			return "", ErrTimeout
		}
		return "", fmt.Errorf("%w: %v", ErrInvalidReply, err)
	}
	if reply.Len() == 0 {
		return "", ErrEmptyReply
	}
	return reply.String(), nil
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
