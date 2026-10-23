// Package webdav implements the minimal WebDAV surface recitebook needs:
// download one file, overwrite one file, and create its parent folder.
package webdav

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	// maxResponseBytes caps a download. The sync file is a small JSON snapshot;
	// anything bigger is a misconfigured URL (or an unrelated file).
	maxResponseBytes = 8 << 20
	requestTimeout   = 20 * time.Second
	userAgent        = "recitebook"
)

// Config holds the user's WebDAV endpoint and credentials.
type Config struct {
	URL      string
	Username string
	Password string
}

// Client talks to a single WebDAV file URL.
type Client struct {
	cfg      Config
	endpoint *url.URL
	err      error
	http     *http.Client
}

// ValidateURL checks that raw is a complete file URL: http or https, with a
// host, a non-empty path, and no trailing slash.
func ValidateURL(raw string) error {
	_, err := parseEndpoint(raw)
	return err
}

func parseEndpoint(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("WebDAV 地址不能为空，请填写完整的文件地址")
	}
	u, err := url.Parse(s)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, errors.New("WebDAV 地址不正确，请填写完整的文件地址，例如 https://dav.jianguoyun.com/dav/recitebook/progress.json")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("WebDAV 地址必须以 http:// 或 https:// 开头")
	}
	if u.Path == "" {
		return nil, errors.New("WebDAV 地址必须指向具体文件，不能是目录")
	}
	if strings.HasSuffix(u.Path, "/") {
		return nil, errors.New("WebDAV 地址必须以文件名结尾，不能以 / 结尾")
	}
	return u, nil
}

// New returns a client for cfg. An invalid URL is kept as a client error so
// every operation reports it; use Err to check a client before making a call.
func New(cfg Config) *Client {
	c := &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: requestTimeout},
	}
	u, err := parseEndpoint(cfg.URL)
	if err != nil {
		c.err = err
		return c
	}
	c.endpoint = u
	return c
}

// Err reports a configuration error captured by New, if any.
func (c *Client) Err() error {
	return c.err
}

// Get downloads the file. A 404 is not an error: it returns exists=false.
func (c *Client) Get(ctx context.Context) ([]byte, bool, error) {
	if c.err != nil {
		return nil, false, c.err
	}
	req, err := c.newRequest(ctx, http.MethodGet, c.endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("WebDAV 下载失败：%w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			return nil, false, fmt.Errorf("WebDAV 读取文件失败：%w", err)
		}
		if len(body) > maxResponseBytes {
			return nil, false, errors.New("云端文件超过 8 MiB，无法下载")
		}
		return body, true, nil
	case http.StatusNotFound:
		return nil, false, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, false, authError(resp.StatusCode)
	default:
		return nil, false, statusError("下载", resp.StatusCode)
	}
}

// Put overwrites the file. The parent folder must already exist. Any 2xx
// response means the file was stored.
func (c *Client) Put(ctx context.Context, data []byte) error {
	if c.err != nil {
		return c.err
	}
	req, err := c.newRequest(ctx, http.MethodPut, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("WebDAV 上传失败：%w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return authError(resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("云端目录不存在（HTTP %d）：请先在坚果云网页端创建同步目录", resp.StatusCode)
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	default:
		return statusError("上传", resp.StatusCode)
	}
}

// EnsureFolder creates the direct parent folder of the file URL with MKCOL.
// Only 201 (created) and 405 (already exists) mean success.
func (c *Client) EnsureFolder(ctx context.Context) error {
	if c.err != nil {
		return c.err
	}
	parent := *c.endpoint
	parent.Path = path.Dir(c.endpoint.Path)
	parent.RawPath = ""
	parent.RawQuery = ""
	parent.ForceQuery = false
	parent.Fragment = ""

	req, err := c.newRequest(ctx, "MKCOL", &parent, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("WebDAV 创建目录失败：%w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusMethodNotAllowed:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return authError(resp.StatusCode)
	case http.StatusConflict:
		return fmt.Errorf("无法创建云端目录（HTTP %d）：请先在坚果云网页端创建上一级目录", resp.StatusCode)
	default:
		return statusError("创建目录", resp.StatusCode)
	}
}

func (c *Client) newRequest(ctx context.Context, method string, u *url.URL, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("WebDAV 请求构造失败：%w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	return req, nil
}

func authError(status int) error {
	return fmt.Errorf("WebDAV 认证失败（HTTP %d）：请检查用户名与第三方应用密码", status)
}

func statusError(action string, status int) error {
	return fmt.Errorf("WebDAV %s失败（HTTP %d）", action, status)
}
