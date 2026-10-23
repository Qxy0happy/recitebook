// Package grader scores free-form translation answers through an
// OpenAI-compatible chat completion endpoint.
package grader

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Threshold is the score at or above which an answer counts as correct.
const Threshold = 80

var (
	// ErrNotConfigured means the base URL, model or API key is missing.
	ErrNotConfigured = errors.New("grader: 未配置可用的模型与密钥")
	// ErrTimeout means the provider did not answer in time.
	ErrTimeout = errors.New("grader: 请求超时")
	// ErrStatus means the provider answered with a non-200 status.
	ErrStatus = errors.New("grader: 接口返回非 200")
	// ErrInvalidReply means the provider answer cannot be read as score JSON.
	ErrInvalidReply = errors.New("grader: 返回内容无法解析")
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

// Submission is one translation answer to grade.
type Submission struct {
	QuestionID string
	Stem       string
	Answer     string
	Reference  string
}

// Result is the fixed grading payload returned by the model.
type Result struct {
	Score   int      `json:"score"`
	Issues  []string `json:"issues"`
	Comment string   `json:"comment"`
}

// Correct reports whether the score reaches Threshold.
func (r Result) Correct() bool { return r.Score >= Threshold }

// Client sends grading requests and caches successful results by question and
// normalized answer. It is safe for concurrent use.
type Client struct {
	http  *http.Client
	mu    sync.Mutex
	cache map[string]Result
}

// New builds a client. A nil http.Client gets a 20 second timeout.
func New(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{http: httpClient, cache: make(map[string]Result)}
}

// Grade returns the score for one submission. A cached question and answer
// pair is served without sending a request.
func (c *Client) Grade(ctx context.Context, cfg Config, sub Submission) (Result, error) {
	key := cacheKey(sub)
	if cached, ok := c.cached(key); ok {
		return cached, nil
	}
	if !cfg.Complete() {
		return Result{}, ErrNotConfigured
	}
	result, err := c.request(ctx, cfg, sub)
	if err != nil {
		return Result{}, err
	}
	c.store(key, result)
	return result, nil
}

const systemPrompt = `你是考研英语翻译判分器。输入为英文原句、参考译文与学生译文。` +
	`请只输出一个 JSON 对象，不要输出其他文字，格式为：` +
	`{"score": 0 到 100 的整数, "issues": ["扣分点"], "comment": "总评"}。` +
	`评分重点为达意、语法与用词。`

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Temperature float64       `json:"temperature"`
	Messages    []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *Client) request(ctx context.Context, cfg Config, sub Submission) (Result, error) {
	payload, err := json.Marshal(chatRequest{
		Model:       cfg.Model,
		Temperature: 0,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt(sub)},
		},
	})
	if err != nil {
		return Result{}, fmt.Errorf("grader: 构造请求失败: %w", err)
	}

	url := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("grader: 构造请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := c.http.Do(req)
	if err != nil {
		if isTimeout(err) {
			return Result{}, ErrTimeout
		}
		return Result{}, fmt.Errorf("grader: 请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Result{}, fmt.Errorf("%w: %d", ErrStatus, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidReply, err)
	}
	var envelope chatResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidReply, err)
	}
	if len(envelope.Choices) == 0 {
		return Result{}, fmt.Errorf("%w: 缺少 choices", ErrInvalidReply)
	}
	return parseContent(envelope.Choices[0].Message.Content)
}

func parseContent(content string) (Result, error) {
	content = stripFence(strings.TrimSpace(content))
	var result Result
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrInvalidReply, err)
	}
	if result.Score < 0 || result.Score > 100 {
		return Result{}, fmt.Errorf("%w: 分数超出 0–100", ErrInvalidReply)
	}
	if result.Issues == nil {
		result.Issues = []string{}
	}
	return result, nil
}

// stripFence removes a Markdown code fence that some providers wrap JSON in.
func stripFence(content string) string {
	if !strings.HasPrefix(content, "```") {
		return content
	}
	if idx := strings.Index(content, "\n"); idx >= 0 {
		content = content[idx+1:]
	} else {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(content), "```"))
}

func userPrompt(sub Submission) string {
	reference := strings.TrimSpace(sub.Reference)
	if reference == "" {
		reference = "无"
	}
	return "英文原句：" + sub.Stem + "\n参考译文：" + reference + "\n学生译文：" + sub.Answer
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func cacheKey(sub Submission) string {
	return sub.QuestionID + "\x00" + normalize(sub.Answer)
}

func normalize(answer string) string {
	return strings.ToLower(strings.Join(strings.Fields(answer), " "))
}

func (c *Client) cached(key string) (Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result, ok := c.cache[key]
	return result, ok
}

func (c *Client) store(key string, result Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[key] = result
}
