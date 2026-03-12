package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Cassette holds a set of recorded HTTP interactions.
type Cassette struct {
	RecordedAt   time.Time     `json:"recorded_at"`
	Account      string        `json:"account"`
	Target       string        `json:"target"`
	Interactions []Interaction `json:"interactions"`
}

// Interaction pairs a request with its response.
type Interaction struct {
	Request  RecordedRequest  `json:"request"`
	Response RecordedResponse `json:"response"`
}

// RecordedRequest captures the request method, original URL, and canonical key.
type RecordedRequest struct {
	Method       string `json:"method"`
	URL          string `json:"url"`
	CanonicalKey string `json:"canonical_key"`
}

// RecordedResponse captures the status, headers, and body.
type RecordedResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}

// canonicalKey builds a deterministic matching key from method, path, sorted
// query params, and an optional body hash. Host is deliberately excluded —
// each cassette is scoped to one target.
func canonicalKey(method string, u *url.URL, body []byte) string {
	params := u.Query()
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var qparts []string
	for _, k := range keys {
		vs := params[k]
		sort.Strings(vs)
		for _, v := range vs {
			qparts = append(qparts, k+"="+v)
		}
	}

	key := method + " " + u.Path
	if len(qparts) > 0 {
		key += "?" + strings.Join(qparts, "&")
	}
	if len(body) > 0 {
		normalized := normalizeJSON(body)
		h := sha256.Sum256(normalized)
		key += " #" + fmt.Sprintf("%x", h[:8])
	}
	return key
}

func normalizeJSON(data []byte) []byte {
	var v any
	if json.Unmarshal(data, &v) == nil {
		if sorted, err := json.Marshal(v); err == nil {
			return sorted
		}
	}
	return data
}

// rewriteLinkHost replaces the host portion of URLs inside Link headers.
//
//	<https://3.basecampapi.com/99999/projects.json?page=2>; rel="next"
//	becomes
//	<http://127.0.0.1:PORT/99999/projects.json?page=2>; rel="next"
var linkURLRe = regexp.MustCompile(`<https?://[^/]+(/[^>]*)>`)

func rewriteLinkHost(link, proxyHost string) string {
	return linkURLRe.ReplaceAllString(link, "<"+proxyHost+"$1>")
}
