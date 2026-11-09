// Package feedback holds what `basecamp feedback` shares with the rest of the
// CLI: whether Basecamp advertises the feedback endpoint to this person, the
// cached answer, the override that forces it, and the hint error output
// carries while it is advertised.
//
// The feature ships dark. Basecamp answers GET /{account}/feedback.json with
// 200 when feedback is on for the person and 404 when it is off, so the CLI
// advertises nothing — no command in help or completions, no hint — until a
// detection has seen a 200 or BASECAMP_FEEDBACK forces it on.
package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/config"
)

// Path is the account-relative endpoint: GET discovers, POST submits.
const Path = "/feedback.json"

// Surface is what the CLI reports itself as. `basecamp mcp` has no feedback
// tool, so the CLI never sends cli_mcp.
const Surface = "cli"

// MaxDetails is the server's cap on details, in characters.
const MaxDetails = 2000

// Categories are the values the server accepts for category.
var Categories = []string{"bug", "missing_capability", "confusing", "praise", "other"}

// Hint is the line error output carries while feedback is advertised. It
// describes; it never tells the reader to do anything.
const Hint = "If this looks like a problem with Basecamp, `basecamp feedback` reports it to the Basecamp team."

// NotAvailable is everything `basecamp feedback` says when the feature is off.
const NotAvailable = "Feedback is not available."

// EnvOverride forces the feature on (1/true/yes/on) or off (0/false/no/off),
// skipping detection. Any other value is ignored.
const EnvOverride = "BASECAMP_FEEDBACK"

// TTL is how long a detection answer stands before a refresh is due.
const TTL = 24 * time.Hour

// retryAfterFailure spaces out detections that got no answer (network, 5xx,
// auth), so a flaky connection doesn't add a request to every command.
const retryAfterFailure = time.Hour

// RefreshTimeout bounds the background detection a command runs after it
// has already talked to Basecamp.
const RefreshTimeout = 2 * time.Second

const stateFile = "feedback.json"

// Override reports whether BASECAMP_FEEDBACK forces the feature, and which way.
func Override() (enabled, set bool) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvOverride))) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	}
	return false, false
}

// ValidCategory reports whether c is one the server accepts.
func ValidCategory(c string) bool {
	for _, known := range Categories {
		if c == known {
			return true
		}
	}
	return false
}

// entry is one account's detection answer.
type entry struct {
	Advertised bool      `json:"advertised"`
	CheckedAt  time.Time `json:"checked_at,omitzero"`
	// AttemptedAt is the last detection that got no answer.
	AttemptedAt time.Time `json:"attempted_at,omitzero"`
}

type state struct {
	Accounts map[string]entry `json:"accounts"`
}

// Store is the cached detection state, one entry per Basecamp host and
// account. It lives in the CLI's default cache directory rather than a
// profile's, because help is rendered before any profile is resolved.
type Store struct {
	path string
	now  func() time.Time
}

// DefaultStore returns the store in BASECAMP_CACHE_DIR, or the default cache
// directory when that is unset.
func DefaultStore() *Store {
	dir := os.Getenv("BASECAMP_CACHE_DIR")
	if dir == "" {
		dir = config.Default().CacheDir
	}
	return NewStore(dir)
}

// NewStore returns a store kept in dir.
func NewStore(dir string) *Store {
	return &Store{path: filepath.Join(dir, stateFile), now: time.Now}
}

// Key names one account on one Basecamp host.
func Key(baseURL, accountID string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = config.Default().BaseURL
	}
	return baseURL + " " + accountID
}

func (s *Store) load() state {
	st := state{Accounts: map[string]entry{}}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return st
	}
	if json.Unmarshal(data, &st) != nil || st.Accounts == nil {
		return state{Accounts: map[string]entry{}}
	}
	return st
}

// save writes the state atomically. A concurrent process may lose its own
// update to this one; that costs one more detection, nothing else.
func (s *Store) save(st state) {
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), stateFile+".*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), s.path) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// Advertised reports the cached answer for one account. A stale answer still
// counts until a refresh replaces it.
func (s *Store) Advertised(key string) bool {
	return s.load().Accounts[key].Advertised
}

// AnyAdvertised reports whether any account has a cached 200. Help uses this:
// it is rendered before the account is known, and showing the command for an
// account where it is off costs only a "not available" when it is run.
func (s *Store) AnyAdvertised() bool {
	for _, e := range s.load().Accounts {
		if e.Advertised {
			return true
		}
	}
	return false
}

// NeedsRefresh reports whether the account's answer is due for a detection.
func (s *Store) NeedsRefresh(key string) bool {
	e := s.load().Accounts[key]
	now := s.now()
	return now.Sub(e.CheckedAt) >= TTL && now.Sub(e.AttemptedAt) >= retryAfterFailure
}

// Record stores a detection answer for the account.
func (s *Store) Record(key string, advertised bool) {
	st := s.load()
	st.Accounts[key] = entry{Advertised: advertised, CheckedAt: s.now()}
	s.save(st)
}

// recordFailure notes a detection that got no answer, keeping the last one.
func (s *Store) recordFailure(key string) {
	st := s.load()
	e := st.Accounts[key]
	e.AttemptedAt = s.now()
	st.Accounts[key] = e
	s.save(st)
}

// Getter is the slice of the SDK's account client detection needs.
type Getter interface {
	Get(ctx context.Context, path string) (*basecamp.Response, error)
}

// IsNotFound reports whether err is the server's 404 — the feature is off.
func IsNotFound(err error) bool {
	var sdkErr *basecamp.Error
	if errors.As(err, &sdkErr) {
		return sdkErr.HTTPStatus == http.StatusNotFound || sdkErr.Code == basecamp.CodeNotFound
	}
	return false
}

// Detect asks Basecamp whether feedback is on for this person and records the
// answer: 200 means advertised, 404 means not. Any other outcome records a
// failed attempt, keeps the previous answer, and is returned.
func (s *Store) Detect(ctx context.Context, api Getter, key string) (bool, error) {
	_, err := api.Get(ctx, Path)
	switch {
	case err == nil:
		s.Record(key, true)
		return true, nil
	case IsNotFound(err):
		s.Record(key, false)
		return false, nil
	default:
		s.recordFailure(key)
		return false, err
	}
}

// Enabled reports whether feedback is on for an account: the override when it
// is set, the cached answer otherwise.
func (s *Store) Enabled(key string) bool {
	if on, set := Override(); set {
		return on
	}
	return s.Advertised(key)
}

// WantsHint reports whether a failed command's error is one the hint belongs
// on: an answer from Basecamp itself (a server error, a refused request, a
// missing resource). Usage mistakes, auth, rate limits and the network are
// the caller's to fix, so they never carry it.
func WantsHint(err error) bool {
	var sdkErr *basecamp.Error
	if !errors.As(err, &sdkErr) {
		return false
	}
	switch sdkErr.Code {
	case basecamp.CodeAPI, basecamp.CodeValidation, basecamp.CodeNotFound:
		return true
	}
	return false
}

// Visible reports whether the command belongs in help and completions: the
// override when it is set, any cached 200 otherwise.
func (s *Store) Visible() bool {
	if on, set := Override(); set {
		return on
	}
	return s.AnyAdvertised()
}
