package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// replayableHeaders lists response headers worth preserving in cassettes.
// Everything else (Set-Cookie, Server-Timing, CSP nonces, X-Request-Id, etc.)
// is dropped to avoid storing sensitive or non-deterministic values.
var replayableHeaders = map[string]bool{
	"content-type":        true,
	"content-length":      true,
	"content-disposition": true,
	"content-encoding":    true,
	"link":                true,
	"x-total-count":       true,
	"etag":                true,
	"last-modified":       true,
	"cache-control":       true,
	"location":            true,
	"retry-after":         true,
}

func replayableHeader(name string) bool {
	return replayableHeaders[strings.ToLower(name)]
}

type recordingProxy struct {
	target       string
	client       *http.Client
	proxyHost    string
	interactions []Interaction
	keys         map[string]int // total count per key
	mu           sync.Mutex
	cassDir      string
	account      string
}

func newRecordingProxy(targetURL, cassDir, account, proxyHost string) *recordingProxy {
	return &recordingProxy{
		target:    strings.TrimRight(targetURL, "/"),
		client:    &http.Client{Timeout: 30 * time.Second},
		proxyHost: proxyHost,
		cassDir:   cassDir,
		account:   account,
		keys:      make(map[string]int),
	}
}

func (rp *recordingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reqBody, _ := io.ReadAll(r.Body)

	upURL := rp.target + r.URL.RequestURI()
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upURL, bytes.NewReader(reqBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Copy headers, skip hop-by-hop
	for k, vs := range r.Header {
		switch strings.ToLower(k) {
		case "connection", "keep-alive", "proxy-authenticate",
			"proxy-authorization", "te", "trailer",
			"transfer-encoding", "upgrade":
			continue
		}
		for _, v := range vs {
			upReq.Header.Add(k, v)
		}
	}

	resp, err := rp.client.Do(upReq)
	if err != nil {
		log.Printf("upstream error: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Build canonical key from the original (pre-proxy) request path
	key := canonicalKey(r.Method, r.URL, reqBody)

	// Collect response headers (allowlist only — exclude sensitive and
	// non-deterministic headers like Set-Cookie, Server-Timing, CSP nonces)
	headers := make(map[string][]string)
	for k, vs := range resp.Header {
		if !replayableHeader(k) {
			continue
		}
		copied := make([]string, len(vs))
		copy(copied, vs)
		headers[k] = copied
	}

	rp.mu.Lock()
	rp.keys[key]++
	count := rp.keys[key]
	rp.interactions = append(rp.interactions, Interaction{
		Request: RecordedRequest{
			Method:       r.Method,
			URL:          upURL,
			CanonicalKey: key,
		},
		Response: RecordedResponse{
			Status:  resp.StatusCode,
			Headers: headers,
			Body:    string(respBody),
		},
	})
	rp.mu.Unlock()

	if count > 1 {
		log.Printf("duplicate #%d for key: %s", count, strings.ReplaceAll(key, "\n", " "))
	}

	// Write response to client with Link headers rewritten to proxy
	for k, vs := range resp.Header {
		for _, v := range vs {
			if strings.EqualFold(k, "Link") {
				v = rewriteLinkHost(v, rp.proxyHost)
			}
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func (rp *recordingProxy) save() error {
	rp.mu.Lock()
	snapshot := make([]Interaction, len(rp.interactions))
	copy(snapshot, rp.interactions)
	rp.mu.Unlock()

	c := Cassette{
		RecordedAt:   time.Now().UTC(),
		Account:      rp.account,
		Target:       rp.target,
		Interactions: snapshot,
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(rp.cassDir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(rp.cassDir, "cassette.json")
	log.Printf("saved %d interactions to %s", len(snapshot), path)
	return os.WriteFile(path, data, 0o600)
}
