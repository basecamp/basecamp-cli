package summarize

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// redirectTransport sends every request to the test server, so providers with
// a fixed base URL (Anthropic) can be exercised without the network.
type redirectTransport struct{ target *url.URL }

func (r redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = r.target.Scheme
	req.URL.Host = r.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

func fakeServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProviders_ReportTruncation(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantText  string
		wantTrunc bool
		complete  func(srv *httptest.Server) (string, error)
	}{
		{
			name: "anthropic max_tokens", body: `{"content":[{"text":"cut of"}],"stop_reason":"max_tokens"}`,
			wantText: "cut of", wantTrunc: true,
			complete: func(srv *httptest.Server) (string, error) {
				u, _ := url.Parse(srv.URL)
				p := NewAnthropicProvider("k", "m")
				p.client = &http.Client{Transport: redirectTransport{u}}
				return p.Complete(context.Background(), "hi", 66)
			},
		},
		{
			name: "anthropic end_turn", body: `{"content":[{"text":"Done."}],"stop_reason":"end_turn"}`,
			wantText: "Done.",
			complete: func(srv *httptest.Server) (string, error) {
				u, _ := url.Parse(srv.URL)
				p := NewAnthropicProvider("k", "m")
				p.client = &http.Client{Transport: redirectTransport{u}}
				return p.Complete(context.Background(), "hi", 66)
			},
		},
		{
			name: "openai length", body: `{"choices":[{"message":{"content":"cut of"},"finish_reason":"length"}]}`,
			wantText: "cut of", wantTrunc: true,
			complete: func(srv *httptest.Server) (string, error) {
				return NewOpenAIProvider(srv.URL, "", "m").Complete(context.Background(), "hi", 66)
			},
		},
		{
			name: "openai stop", body: `{"choices":[{"message":{"content":"Done."},"finish_reason":"stop"}]}`,
			wantText: "Done.",
			complete: func(srv *httptest.Server) (string, error) {
				return NewOpenAIProvider(srv.URL, "", "m").Complete(context.Background(), "hi", 66)
			},
		},
		{
			name: "ollama length", body: `{"response":"cut of","done":true,"done_reason":"length"}`,
			wantText: "cut of", wantTrunc: true,
			complete: func(srv *httptest.Server) (string, error) {
				return NewOllamaProvider(srv.URL, "m").Complete(context.Background(), "hi", 66)
			},
		},
		{
			name: "ollama stop", body: `{"response":"Done.","done":true,"done_reason":"stop"}`,
			wantText: "Done.",
			complete: func(srv *httptest.Server) (string, error) {
				return NewOllamaProvider(srv.URL, "m").Complete(context.Background(), "hi", 66)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.complete(fakeServer(t, tt.body))
			if got != tt.wantText {
				t.Errorf("text = %q, want %q", got, tt.wantText)
			}
			if gotTrunc := errors.Is(err, ErrTruncated); gotTrunc != tt.wantTrunc {
				t.Errorf("truncated = %v (err %v), want %v", gotTrunc, err, tt.wantTrunc)
			}
			if !tt.wantTrunc && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
