package skylight

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// APIError is a non-2xx response from the Skylight API.
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("skylight: %s %s returned %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// Client makes authenticated Skylight API requests. On a 401 it refreshes the
// access token once and retries. It is safe for concurrent use.
type Client struct {
	baseURL   string
	http      *http.Client
	oauth     OAuth
	logger    *slog.Logger
	onRefresh func(Token)

	mu    sync.Mutex
	token Token
}

// Option configures a [Client].
type Option func(*Client)

// WithBaseURL overrides [DefaultBaseURL].
func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(u, "/") }
}

// WithHTTPClient sets the HTTP client used for API and token requests.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// WithDeviceFingerprint sets the fingerprint sent on token refresh. It should
// be the same one used at login.
func WithDeviceFingerprint(fp string) Option {
	return func(c *Client) { c.oauth.DeviceFingerprint = fp }
}

// WithTokenRefreshHook is called with the new token pair after every refresh,
// so the caller can persist it. Skylight may rotate the refresh token.
func WithTokenRefreshHook(fn func(Token)) Option {
	return func(c *Client) { c.onRefresh = fn }
}

// WithLogger enables debug logging of requests and full response bodies.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) { c.logger = l }
}

// NewClient returns a Client authenticated with tok.
func NewClient(tok Token, opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		http:    &http.Client{Timeout: 20 * time.Second},
		logger:  slog.New(slog.DiscardHandler),
		token:   tok,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.oauth.BaseURL = c.baseURL
	c.oauth.HTTPClient = c.http
	return c
}

// Token returns the current token pair, which may have been refreshed since
// the client was created.
func (c *Client) Token() Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}

// do sends a request and returns the response body. body, if non-nil, is
// encoded as JSON.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode %s %s body: %w", method, path, err)
		}
	}

	for attempt := 0; ; attempt++ {
		access := c.Token().AccessToken
		status, respBody, err := c.send(ctx, method, path, query, payload, access)
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized && attempt == 0 {
			if err := c.refresh(ctx, access); err != nil {
				return nil, err
			}
			continue
		}
		if status == http.StatusUnauthorized {
			return nil, fmt.Errorf("%w: %s %s still unauthorized after refresh", ErrAuth, method, path)
		}
		if status >= 400 {
			apiErr := &APIError{Method: method, Path: path, StatusCode: status, Body: truncate(respBody, 500)}
			// Skylight's 422 bodies name the bad field but never echo what was
			// sent, which is the only way to tell a wrong key from a wrong shape.
			if status == http.StatusUnprocessableEntity && payload != nil {
				apiErr.Body += " (sent " + string(payload) + ")"
			}
			return nil, apiErr
		}
		return respBody, nil
	}
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, payload []byte, access string) (int, []byte, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Skylight-Api-Version", apiVersion)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("read %s %s response: %w", method, path, err)
	}
	c.logger.DebugContext(ctx, "skylight request",
		"method", method,
		"path", path,
		"status", resp.StatusCode,
		"sent", string(payload),
		"body", string(respBody),
	)
	return resp.StatusCode, respBody, nil
}

// refresh swaps in a new token pair, unless another request already did so
// after staleAccess was read.
func (c *Client) refresh(ctx context.Context, staleAccess string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.AccessToken != staleAccess {
		return nil
	}
	tok, err := c.oauth.Refresh(ctx, c.token.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh access token: %w", err)
	}
	c.token = tok
	c.logger.DebugContext(ctx, "skylight token refreshed")
	if c.onRefresh != nil {
		c.onRefresh(tok)
	}
	return nil
}
