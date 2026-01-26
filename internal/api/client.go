// Package api provides an HTTP client for the Basecamp API.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/basecamp/bcq/internal/auth"
	"github.com/basecamp/bcq/internal/config"
	"github.com/basecamp/bcq/internal/output"
	"github.com/basecamp/bcq/internal/version"
)

// Default values for client configuration.
const (
	DefaultMaxRetries = 5
	DefaultBaseDelay  = 1 * time.Second
	DefaultMaxJitter  = 100 * time.Millisecond
	DefaultTimeout    = 30 * time.Second
	DefaultMaxPages   = 10000
)

// ClientOptions configures the API client behavior.
type ClientOptions struct {
	// HTTP settings
	Timeout   time.Duration // Request timeout (default: 30s)
	Transport http.RoundTripper

	// Retry settings
	MaxRetries int           // Maximum retry attempts for GET requests (default: 5)
	BaseDelay  time.Duration // Initial backoff delay (default: 1s)
	MaxJitter  time.Duration // Maximum random jitter to add to delays (default: 100ms)

	// Pagination
	MaxPages int // Maximum pages to fetch in GetAll (default: 10000)

	// Custom User-Agent (appended to default)
	UserAgent string
}

// ClientOption is a functional option for configuring the client.
type ClientOption func(*ClientOptions)

// WithTimeout sets the HTTP request timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(o *ClientOptions) { o.Timeout = d }
}

// WithMaxRetries sets the maximum number of retry attempts.
func WithMaxRetries(n int) ClientOption {
	return func(o *ClientOptions) { o.MaxRetries = n }
}

// WithUserAgent sets a custom User-Agent suffix.
func WithUserAgent(ua string) ClientOption {
	return func(o *ClientOptions) { o.UserAgent = ua }
}

// WithTransport sets a custom HTTP transport.
func WithTransport(t http.RoundTripper) ClientOption {
	return func(o *ClientOptions) { o.Transport = t }
}

// retryableError wraps an error with retry metadata.
type retryableError struct {
	err        error
	retryAfter time.Duration // Server-specified delay (from Retry-After header)
}

func (r *retryableError) Error() string {
	return r.err.Error()
}

func (r *retryableError) Unwrap() error {
	return r.err
}

// Client is an HTTP client for the Basecamp API.
type Client struct {
	httpClient *http.Client
	auth       *auth.Manager
	cfg        *config.Config
	cache      *Cache
	opts       ClientOptions
	logger     *slog.Logger
}

// Response wraps an API response.
type Response struct {
	Data       json.RawMessage
	StatusCode int
	Headers    http.Header
	FromCache  bool
}

// UnmarshalData unmarshals the response data into the given value.
func (r *Response) UnmarshalData(v any) error {
	return json.Unmarshal(r.Data, v)
}

// NewClient creates a new API client.
func NewClient(cfg *config.Config, authMgr *auth.Manager, options ...ClientOption) *Client {
	// Apply defaults
	opts := ClientOptions{
		Timeout:    DefaultTimeout,
		MaxRetries: DefaultMaxRetries,
		BaseDelay:  DefaultBaseDelay,
		MaxJitter:  DefaultMaxJitter,
		MaxPages:   DefaultMaxPages,
	}

	// Apply user options
	for _, opt := range options {
		opt(&opts)
	}

	// Set up transport
	var transport http.RoundTripper
	if opts.Transport != nil {
		transport = opts.Transport
	} else {
		// Clone DefaultTransport to preserve proxy settings, HTTP/2, dial timeouts, TLS config
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.MaxIdleConns = 100
		t.MaxIdleConnsPerHost = 10
		t.IdleConnTimeout = 90 * time.Second
		transport = t
	}

	// Normalize BaseURL to prevent double slashes and cache key mismatches
	cfg.BaseURL = config.NormalizeBaseURL(cfg.BaseURL)

	return &Client{
		httpClient: &http.Client{
			Timeout:   opts.Timeout,
			Transport: transport,
		},
		auth:  authMgr,
		cfg:   cfg,
		cache: NewCache(cfg.CacheDir),
		opts:  opts,
	}
}

// SetLogger sets the structured logger for debug output.
func (c *Client) SetLogger(l *slog.Logger) {
	c.logger = l
}

// log outputs a debug message if a logger is configured.
func (c *Client) log(msg string, args ...any) {
	if c.logger != nil {
		c.logger.Debug(msg, args...)
	}
}

// warn outputs a warning message if a logger is configured.
func (c *Client) warn(msg string, args ...any) {
	if c.logger != nil {
		c.logger.Warn(msg, args...)
	}
}

// Get performs a GET request.
func (c *Client) Get(ctx context.Context, path string) (*Response, error) {
	return c.doRequest(ctx, "GET", path, nil)
}

// Post performs a POST request with a JSON body.
func (c *Client) Post(ctx context.Context, path string, body any) (*Response, error) {
	return c.doRequest(ctx, "POST", path, body)
}

// Put performs a PUT request with a JSON body.
func (c *Client) Put(ctx context.Context, path string, body any) (*Response, error) {
	return c.doRequest(ctx, "PUT", path, body)
}

// Delete performs a DELETE request.
func (c *Client) Delete(ctx context.Context, path string) (*Response, error) {
	return c.doRequest(ctx, "DELETE", path, nil)
}

// GetAll fetches all pages for a paginated resource.
func (c *Client) GetAll(ctx context.Context, path string) ([]json.RawMessage, error) {
	var allResults []json.RawMessage
	url := c.buildURL(path)
	page := 0

	for page = 1; page <= c.opts.MaxPages; page++ {
		resp, err := c.doRequestURL(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}

		// Parse response as array
		var items []json.RawMessage
		if err := json.Unmarshal(resp.Data, &items); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		allResults = append(allResults, items...)

		// Check for next page
		nextURL := parseNextLink(resp.Headers.Get("Link"))
		if nextURL == "" {
			break
		}
		url = nextURL
	}

	if page > c.opts.MaxPages {
		c.warn("pagination capped", "maxPages", c.opts.MaxPages)
	}

	return allResults, nil
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any) (*Response, error) {
	url := c.buildURL(path)
	return c.doRequestURL(ctx, method, url, body)
}

func (c *Client) doRequestURL(ctx context.Context, method, url string, body any) (*Response, error) {
	// Non-GET requests: don't retry on 429/5xx (could duplicate data), but DO retry
	// once after a successful 401 token refresh so users don't have to rerun commands.
	if method != "GET" {
		resp, err := c.singleRequest(ctx, method, url, body, 1)
		if err == nil {
			return resp, nil
		}
		// Only retry if this was a 401 that triggered successful token refresh
		if apiErr, ok := err.(*output.Error); ok && apiErr.Retryable && apiErr.Code == output.CodeAuth {
			c.log("token refreshed, retrying", "method", method)
			return c.singleRequest(ctx, method, url, body, 2)
		}
		return nil, err
	}

	var attempt int
	var lastErr error

	for attempt = 1; attempt <= c.opts.MaxRetries; attempt++ {
		resp, err := c.singleRequest(ctx, method, url, body, attempt)
		if err == nil {
			return resp, nil
		}

		// Check for retryable error with server-specified delay
		var delay time.Duration
		if re, ok := err.(*retryableError); ok {
			lastErr = re.err
			if re.retryAfter > 0 {
				// Use server-specified Retry-After delay
				delay = re.retryAfter
			} else {
				delay = c.backoffDelay(attempt)
			}
		} else if apiErr, ok := err.(*output.Error); ok {
			if !apiErr.Retryable {
				return nil, err
			}
			lastErr = err
			delay = c.backoffDelay(attempt)
		} else {
			return nil, err
		}

		c.log("retrying", "attempt", attempt, "max", c.opts.MaxRetries, "delay", delay, "error", lastErr)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			continue
		}
	}

	return nil, fmt.Errorf("request failed after %d retries: %w", c.opts.MaxRetries, lastErr)
}

func (c *Client) singleRequest(ctx context.Context, method, url string, body any, attempt int) (*Response, error) {
	// Get access token
	token, err := c.auth.AccessToken(ctx)
	if err != nil {
		return nil, err
	}

	// Build request body
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal body: %w", err)
		}
		bodyReader = strings.NewReader(string(bodyBytes))
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	ua := version.UserAgent()
	if c.opts.UserAgent != "" {
		ua += " " + c.opts.UserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Add ETag for cached GET requests
	var cacheKey string
	if method == "GET" && c.cfg.CacheEnabled {
		cacheKey = c.cache.Key(url, c.cfg.AccountID, token)
		if etag := c.cache.GetETag(cacheKey); etag != "" {
			req.Header.Set("If-None-Match", etag)
			c.log("cache conditional", "etag", etag)
		}
	}

	c.log("http request", "method", method, "url", url, "attempt", attempt)

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, output.ErrNetwork(err)
	}
	defer resp.Body.Close()

	c.log("http response", "status", resp.StatusCode)

	// Handle response based on status code
	switch resp.StatusCode {
	case http.StatusNotModified: // 304
		if cacheKey != "" {
			cached := c.cache.GetBody(cacheKey)
			if cached != nil {
				c.log("cache hit")
				return &Response{
					Data:       cached,
					StatusCode: http.StatusOK,
					Headers:    resp.Header,
					FromCache:  true,
				}, nil
			}
			// Cache corrupted (etag exists but body missing)—invalidate and refetch
			c.log("cache corrupted, refetching")
			_ = c.cache.Invalidate(cacheKey) // Best-effort invalidation
			// Retry without If-None-Match header by recursing with fresh attempt
			return c.singleRequest(ctx, method, url, body, attempt)
		}
		return nil, output.ErrAPI(304, "304 received but no cached response available")

	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %w", err)
		}

		// Cache GET responses with ETag
		if method == "GET" && cacheKey != "" {
			if etag := resp.Header.Get("ETag"); etag != "" {
				_ = c.cache.Set(cacheKey, respBody, etag) // Best-effort cache write
				c.log("cache stored", "etag", etag)
			}
		}

		return &Response{
			Data:       respBody,
			StatusCode: resp.StatusCode,
			Headers:    resp.Header,
		}, nil

	case http.StatusTooManyRequests: // 429
		retryAfterSecs := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, &retryableError{
			err:        output.ErrRateLimit(retryAfterSecs),
			retryAfter: time.Duration(retryAfterSecs) * time.Second,
		}

	case http.StatusUnauthorized: // 401
		// Try token refresh on first 401
		if attempt == 1 {
			if err := c.auth.Refresh(ctx); err == nil {
				// Retry with new token
				return nil, &output.Error{
					Code:      output.CodeAuth,
					Message:   "Token refreshed",
					Retryable: true,
				}
			}
		}
		return nil, output.ErrAuth("Authentication failed")

	case http.StatusForbidden: // 403
		// Check if this might be a scope issue
		if method != "GET" {
			return nil, output.ErrForbiddenScope()
		}
		return nil, output.ErrForbidden("Access denied")

	case http.StatusNotFound: // 404
		return nil, output.ErrNotFound("Resource", url)

	case http.StatusInternalServerError: // 500
		return nil, output.ErrAPI(500, "Server error (500)")

	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout: // 502, 503, 504
		return nil, &output.Error{
			Code:       output.CodeAPI,
			Message:    fmt.Sprintf("Gateway error (%d)", resp.StatusCode),
			HTTPStatus: resp.StatusCode,
			Retryable:  true,
		}

	default:
		respBody, _ := io.ReadAll(resp.Body)
		var apiErr struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(respBody, &apiErr) == nil {
			msg := apiErr.Error
			if msg == "" {
				msg = apiErr.Message
			}
			if msg != "" {
				return nil, output.ErrAPI(resp.StatusCode, msg)
			}
		}
		return nil, output.ErrAPI(resp.StatusCode, fmt.Sprintf("Request failed (HTTP %d)", resp.StatusCode))
	}
}

func (c *Client) buildURL(path string) string {
	// Ensure path starts with /
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	// If path already has account ID prefix, use it directly
	if strings.HasPrefix(path, "/"+c.cfg.AccountID+"/") {
		return c.cfg.BaseURL + path
	}

	// Check if this is an account-relative path (most API calls)
	// Skip account ID for authorization endpoints
	if strings.HasPrefix(path, "/.well-known/") || strings.HasPrefix(path, "/authorization/") {
		return c.cfg.BaseURL + path
	}

	// Add account ID for regular API paths
	if c.cfg.AccountID != "" {
		return c.cfg.BaseURL + "/" + c.cfg.AccountID + path
	}

	return c.cfg.BaseURL + path
}

func (c *Client) backoffDelay(attempt int) time.Duration {
	// Exponential backoff: base * 2^(attempt-1)
	delay := c.opts.BaseDelay * time.Duration(1<<(attempt-1))

	// Add jitter
	if c.opts.MaxJitter > 0 {
		jitter := time.Duration(rand.Int63n(int64(c.opts.MaxJitter))) //nolint:gosec // G404: Jitter doesn't need crypto rand
		delay += jitter
	}

	return delay
}

// parseNextLink extracts the next URL from a Link header.
// Example: <https://...?page=2>; rel="next", <https://...?page=5>; rel="last"
func parseNextLink(linkHeader string) string {
	if linkHeader == "" {
		return ""
	}

	for _, part := range strings.Split(linkHeader, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, `rel="next"`) {
			// Extract URL between < and >
			start := strings.Index(part, "<")
			end := strings.Index(part, ">")
			if start >= 0 && end > start {
				return part[start+1 : end]
			}
		}
	}

	return ""
}

// parseRetryAfter parses the Retry-After header value.
func parseRetryAfter(header string) int {
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		return seconds
	}
	return 0
}

// ProjectPath builds a path relative to a project (bucket).
func (c *Client) ProjectPath(resource string) string {
	if c.cfg.ProjectID == "" {
		return ""
	}
	return "/buckets/" + c.cfg.ProjectID + resource
}

// RequireProject returns an error if no project is configured.
func (c *Client) RequireProject() error {
	if c.cfg.ProjectID == "" {
		return output.ErrUsageHint(
			"No project specified",
			"Use --project or set project_id in .basecamp/config.json",
		)
	}
	return nil
}

// RequireAccount returns an error if no account is configured.
func (c *Client) RequireAccount() error {
	if c.cfg.AccountID == "" {
		return output.ErrUsageHint(
			"No account configured",
			"Set BASECAMP_ACCOUNT_ID or run: bcq config set account_id <id>",
		)
	}
	return nil
}
