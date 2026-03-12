package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type replayServer struct {
	interactions []Interaction
	cursors      map[string]int
	mu           sync.Mutex
	proxyHost    string
}

func newReplayServer(cassDir string) (*replayServer, error) {
	rs := &replayServer{cursors: make(map[string]int)}

	entries, err := os.ReadDir(cassDir)
	if err != nil {
		return nil, fmt.Errorf("reading cassette dir: %w", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cassDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", e.Name(), err)
		}
		var c Cassette
		if err := json.Unmarshal(data, &c); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", e.Name(), err)
		}
		rs.interactions = append(rs.interactions, c.Interactions...)
	}

	log.Printf("loaded %d interactions from %s", len(rs.interactions), cassDir)
	return rs, nil
}

func (rs *replayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	key := canonicalKey(r.Method, r.URL, body)

	rs.mu.Lock()
	cursor := rs.cursors[key]

	var matches []int
	for i, ix := range rs.interactions {
		if ix.Request.CanonicalKey == key {
			matches = append(matches, i)
		}
	}

	if len(matches) == 0 {
		rs.mu.Unlock()
		log.Printf("no match for key")
		http.Error(w, fmt.Sprintf("no cassette match: %s", key), http.StatusBadGateway)
		return
	}

	if cursor >= len(matches) {
		rs.mu.Unlock()
		log.Printf("cursor exhausted (%d entries)", len(matches))
		http.Error(w, fmt.Sprintf("cursor exhausted: %s (had %d)", key, len(matches)), http.StatusInternalServerError)
		return
	}

	ix := rs.interactions[matches[cursor]]
	rs.cursors[key] = cursor + 1
	rs.mu.Unlock()

	for k, vs := range ix.Response.Headers {
		for _, v := range vs {
			if strings.EqualFold(k, "Link") && rs.proxyHost != "" {
				v = rewriteLinkHost(v, rs.proxyHost)
			}
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(ix.Response.Status)
	fmt.Fprint(w, ix.Response.Body)
}
