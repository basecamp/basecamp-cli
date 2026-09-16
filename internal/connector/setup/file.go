// Package setup is `basecamp connect setup`'s half of the connector (plan
// step 16): connect.json — its schema, where it lives, how it is written
// and how it is refused — and the checks that can run before the connector
// does.
//
// # connect.json
//
// connect.json is the only authority for which directory a project maps to
// and for who may drive the agent. It is local policy: nothing read from
// Basecamp adds a route or widens trust. Its admission part (trust and
// projects) is exactly what admission.ParsePolicy reads; the rest belongs to
// dispatch.
//
// Because the file is the trust anchor, a copy another user could have
// written is not one to act on. Save writes it owner-only (0600, in 0700
// directories) and atomically; Load refuses a file or directory another
// user owns or can write, a symlink, and anything that is not a regular
// file. A policy the connector would misread is refused at both ends.
//
// # What is not here
//
// The agent's credential. Setup runs on a profile that already holds it
// (connected with `basecamp auth agent connect`, or a bot user logged in
// with `basecamp auth login --expect-identity`), and never stores, replaces
// or removes one. connect.json records only the identity that credential
// proved, so a later run can refuse a profile that has since been pointed at
// someone else.
package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/admission"
)

// Version is the connect.json schema version this package writes and reads.
const Version = 1

// FileName is connect.json's name inside its profile directory.
const FileName = "connect.json"

// Drivers.
const (
	DriverSpawn = "spawn"
	DriverACP   = "acp"
)

// Defaults, from the connector spec.
const (
	DefaultDriver      = DriverSpawn
	DefaultConcurrency = 2
	DefaultDeadline    = 45 * time.Minute

	MaxConcurrency = 32
	MinDeadline    = time.Minute
	MaxDeadline    = 24 * time.Hour
)

// Credential kinds recorded for the agent.
const (
	// KindAgent is an Agent person holding a client_credentials profile, the
	// connection ceremony's result.
	KindAgent = "agent"
	// KindBotUser is a person login (a device-flow bot user) pinned by
	// --expect-identity: version 1's path.
	KindBotUser = "bot_user"
)

// File is connect.json.
type File struct {
	Version int `json:"version"`

	// Profile is the CLI profile holding the agent's credential.
	Profile string `json:"profile"`
	// AccountID is the account the agent belongs to.
	AccountID string `json:"account_id"`
	// Agent is who the profile's credential proved to be when setup ran.
	Agent Agent `json:"agent"`

	// Trust and Projects are admission's, in admission's own types, so the
	// two cannot drift.
	Trust    admission.Trust           `json:"trust"`
	Projects map[int64]admission.Route `json:"projects"`

	Driver      string   `json:"driver"`
	Concurrency int      `json:"concurrency"`
	Deadline    Duration `json:"deadline"`
	Worktrees   bool     `json:"worktrees"`
}

// Agent is the identity the agent profile authenticated as at setup.
type Agent struct {
	// PersonID is the agent's Person id in AccountID. Admission takes the
	// agent's id from the profile's verified identity at run time; this is
	// what that identity is checked against.
	PersonID int64 `json:"person_id"`
	// Kind is KindAgent or KindBotUser.
	Kind string `json:"kind"`
	// IdentityID is the bot user's account-independent identity, the value
	// --expect-identity pins. Zero for an Agent person, which has none.
	IdentityID int64 `json:"identity_id,omitempty"`
}

// Duration is a time.Duration written as Go's duration string ("45m0s").
type Duration time.Duration

// MarshalJSON implements json.Marshaler.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("deadline must be a duration string such as \"45m\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("deadline: %w", err)
	}
	*d = Duration(v)
	return nil
}

// New is an empty connect.json for a profile, with the spec's defaults.
func New(profile string) File {
	return File{
		Version:     Version,
		Profile:     profile,
		Trust:       admission.Trust{Mode: admission.TrustOperator},
		Projects:    map[int64]admission.Route{},
		Driver:      DefaultDriver,
		Concurrency: DefaultConcurrency,
		Deadline:    Duration(DefaultDeadline),
	}
}

// Path is where a profile's connect.json lives: under the CLI's config
// directory, one directory per profile, since a connector runs as one
// profile's agent.
func Path(configDir, profile string) (string, error) {
	if !validProfile.MatchString(profile) {
		return "", fmt.Errorf("connect.json needs a profile name of letters, numbers, hyphens and underscores, got %q", profile)
	}
	return filepath.Join(configDir, "connect", profile, FileName), nil
}

var (
	validProfile = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	validClass   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
)

// ValidClass reports whether a class is one connect.json can carry: it is
// written onto every stdout pointer line, so it stays a short plain token.
func ValidClass(class string) bool { return validClass.MatchString(class) }

// Policy is the admission policy this file yields for the agent, obtained
// the way the connector obtains it: by serializing the file and parsing it
// with admission.ParsePolicy, then validating. A file whose policy does not
// validate is one admission would refuse, so setup refuses to write it.
func (f File) Policy(agentID int64) (admission.Policy, error) {
	data, err := json.Marshal(f)
	if err != nil {
		return admission.Policy{}, err
	}
	p, err := admission.ParsePolicy(data)
	if err != nil {
		return admission.Policy{}, err
	}
	p.AgentID = agentID
	if err := p.Validate(); err != nil {
		return admission.Policy{}, err
	}
	return p, nil
}

// Validate refuses a file the connector could misread. It covers everything
// Policy does and the dispatch settings besides.
func (f File) Validate() error {
	switch {
	case f.Version != Version:
		return fmt.Errorf("connect.json version %d is not %d", f.Version, Version)
	case !validProfile.MatchString(f.Profile):
		return fmt.Errorf("connect.json names no valid profile (%q)", f.Profile)
	case !isAccountID(f.AccountID):
		return fmt.Errorf("connect.json account_id %q is not a numeric account id", f.AccountID)
	case f.Agent.PersonID <= 0:
		return errors.New("connect.json records no agent person id")
	case f.Trust.Mode == "":
		return errors.New("connect.json names no trust mode")
	}
	switch f.Agent.Kind {
	case KindAgent:
		if f.Agent.IdentityID != 0 {
			return errors.New("connect.json records an identity id for an Agent person, which has none")
		}
	case KindBotUser:
		if f.Agent.IdentityID <= 0 {
			return errors.New("connect.json records a bot user without the identity id that pins it")
		}
	default:
		return fmt.Errorf("connect.json agent kind %q is not %q or %q", f.Agent.Kind, KindAgent, KindBotUser)
	}
	if _, err := f.Policy(f.Agent.PersonID); err != nil {
		return err
	}
	for bucket, route := range f.Projects {
		if !filepath.IsAbs(route.Path) || filepath.Clean(route.Path) != route.Path {
			return fmt.Errorf("route for project %d: path %q is not a clean absolute path", bucket, route.Path)
		}
		if route.Class != "" && !ValidClass(route.Class) {
			return fmt.Errorf("route for project %d: class %q must be lowercase letters, digits, - or _, at most 40", bucket, route.Class)
		}
	}
	switch f.Driver {
	case DriverSpawn, DriverACP:
	default:
		return fmt.Errorf("connect.json driver %q is not %q or %q", f.Driver, DriverSpawn, DriverACP)
	}
	if f.Concurrency < 1 || f.Concurrency > MaxConcurrency {
		return fmt.Errorf("connect.json concurrency %d is outside 1..%d", f.Concurrency, MaxConcurrency)
	}
	if d := time.Duration(f.Deadline); d < MinDeadline || d > MaxDeadline {
		return fmt.Errorf("connect.json deadline %s is outside %s..%s", d, MinDeadline, MaxDeadline)
	}
	return nil
}

// Parse decodes connect.json strictly. It refuses what encoding/json would
// quietly accept: an unknown key (a misspelled "watch_completion" ignored is
// a project the operator believes is driven and is not), a key given twice
// (only the last would count, so the file would not say what it reads as),
// and anything after the object.
func Parse(data []byte) (File, error) {
	if err := refuseDuplicateKeys(data); err != nil {
		return File{}, fmt.Errorf("parse connect.json: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("parse connect.json: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return File{}, errors.New("parse connect.json: trailing data after the object")
	}
	if f.Projects == nil {
		f.Projects = map[int64]admission.Route{}
	}
	if err := f.Validate(); err != nil {
		return File{}, err
	}
	return f, nil
}

// canonicalKey reports whether a key is in its one canonical spelling:
// lowercase letters, digits and underscores for names, plain decimal for
// project ids.
func canonicalKey(key string) bool {
	if n, err := strconv.ParseInt(key, 10, 64); err == nil {
		return strconv.FormatInt(n, 10) == key
	}
	if key == "" {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}

// refuseDuplicateKeys walks the JSON document and refuses any object that
// names a key twice. encoding/json matches field names case-insensitively
// and parses project ids as numbers, so "Trust" is "trust" and "048699913"
// is 48699913 to it; every key must therefore be in its one canonical
// spelling, which makes an exact comparison a complete one.
func refuseDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := keyTok.(string)
				if !canonicalKey(key) {
					return fmt.Errorf("key %q is not spelled canonically: names are lowercase, project ids plain decimal", key)
				}
				if seen[key] {
					return fmt.Errorf("key %q appears twice in one object", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		}
		_, err = dec.Token() // the closing delimiter
		return err
	}
	return walk()
}

// Load reads and validates the connect.json at path. A file that does not
// exist is reported as os.ErrNotExist (use errors.Is). A file this user does
// not solely control is refused before a byte of it is believed.
func Load(path string) (File, error) {
	data, err := readPrivate(path)
	if err != nil {
		return File{}, err
	}
	return Parse(data)
}

// Save validates f and writes it to path atomically, owner-only.
func Save(path string, f File) error {
	if err := f.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

// isAccountID accepts an account id only in its canonical spelling, the one
// the connector's instance lock is keyed on.
func isAccountID(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}
