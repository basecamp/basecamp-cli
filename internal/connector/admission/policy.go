package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// TrustMode names who, besides the operator, may drive the agent.
type TrustMode string

const (
	// TrustOperator trusts the operator alone. The default.
	TrustOperator TrustMode = "operator"
	// TrustAllowlist trusts the operator and a list of Person ids.
	TrustAllowlist TrustMode = "allowlist"
	// TrustProject trusts the operator and any non-client member of the
	// event's project.
	TrustProject TrustMode = "project"
)

// Trust is the trust section of connect.json.
type Trust struct {
	Mode TrustMode `json:"mode"`
	// OperatorID is the Person id of the person who approved the connection.
	OperatorID int64 `json:"operator_id"`
	// AllowlistIDs are the Person ids trusted in allowlist mode.
	AllowlistIDs []int64 `json:"allowlist_ids,omitempty"`
}

// Project is one served project's entry in connect.json: a project the agent
// may be driven from, and how admission treats it. The entries are local:
// nothing read from Basecamp can add or change one, which is what makes the
// list an answer to "which projects may drive this agent" that only the
// operator writes. It matters most under TrustProject, where any non-client
// member of a served project can drive the agent.
//
// No directory is associated with a project. The connector runs where it was
// started; a task that needs a clone or a directory of its own is the agent's
// business to make.
type Project struct {
	// Class is the project's classification, carried on the record.
	Class string `json:"class,omitempty"`
	// WatchCompletions makes the agent a driver of the project: every trusted
	// completion in it is admitted as trigger completed, without the agent
	// being assigned or subscribed.
	WatchCompletions bool `json:"watch_completions,omitempty"`

	// LegacyPath is the directory a connector that routed projects to
	// directories ran this project's work in. Nothing reads it: a task runs
	// where the connector was started. It is still a field because
	// setup.Parse refuses an unknown key, and every connect.json written
	// before the paths went has "path" on every project — deleting the field
	// outright would stop every connector already set up. setup.Parse zeroes
	// it, and omitempty keeps it out of everything written from here, so the
	// next `connect setup` writes the key away for good.
	LegacyPath string `json:"path,omitempty"`
}

// UnmarshalJSON refuses a null entry, and decodes an object strictly.
//
// Both matter, and the first is the one that bites. A JSON null decodes into
// a struct as the zero value without an error, so `"projects":{"123":null}`
// would read as project 123 served with no settings. Nothing noticed that
// before this file stopped carrying a path, because the path was required
// and a null has none — the check was doing two jobs and looked like it was
// doing one. connect.json is the trust anchor: a half-edited file, a bad
// merge or a truncated write has to withhold authorization, not grant it
// (Copilot on #765).
//
// The strictness is the second job the outer decoder used to do here.
// setup.Parse refuses an unknown key so that a misspelled watch_completion
// is a refusal rather than a project the operator believes is driven and is
// not — and a type's own UnmarshalJSON does not inherit that setting, so it
// is applied again here. It binds admission's reader too, which is the
// narrower of the two behaviors and the right one: the keys a project entry
// may carry are all known here, unlike the file's own, which carry other
// steps' settings.
func (p *Project) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		return errors.New("a served project's entry is null; an entry is an object, {} for one with no settings")
	}
	// A local type to shed this method, or decoding recurses.
	type project Project
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw project
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if err := checkLegacyPath(data); err != nil {
		return err
	}
	*p = Project(raw)
	return nil
}

// checkLegacyPath holds a path key that is present to the shape the writer
// that produced it could only have written: a clean absolute path.
//
// LegacyPath is decoded and thrown away, which is not the same as not caring
// what it is. A connector that routed projects wrote nothing but clean
// absolute paths there, and setup refused a file carrying anything else — so
// a null, an empty string, a relative or an unclean one means the file is not
// what it claims to be, and the files the old validation refused would
// otherwise be the ones that now authorize their projects (Copilot on #765).
//
// A key that is absent is a file written since the paths went, and is left
// alone.
//
// The key is found case-insensitively, the way encoding/json found it. An
// exact lookup of "path" is not the same question as "did a value reach
// LegacyPath": the decoder matches a struct tag without regard to case, so
// {"Path": "../x"} fills the field while an exact lookup sees an absent key
// and validates nothing (Copilot on #765). CheckCanonicalKeys refuses that
// spelling for every document either reader parses, so this is the second
// of two; it is here because Project is exported and its UnmarshalJSON has
// to fail closed for whoever calls it, not only for callers that walked the
// document first. A guarantee that depends on the order two functions run
// in is one a later edit can take away without touching either.
func checkLegacyPath(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	keys := make([]string, 0, len(probe))
	for key := range probe {
		if strings.EqualFold(key, "path") {
			keys = append(keys, key)
		}
	}
	// Sorted: a map ranges in a random order, and an error message that
	// varies run to run is one a test cannot assert and a person cannot
	// report. Only an unwalked document can hold two of these at once.
	slices.Sort(keys)
	for _, key := range keys {
		if err := checkLegacyPathValue(probe[key]); err != nil {
			return err
		}
	}
	return nil
}

func checkLegacyPathValue(raw json.RawMessage) error {
	if string(bytes.TrimSpace(raw)) == "null" {
		return errors.New(`the "path" of a connector that routed projects is null; no such connector wrote that`)
	}
	var legacy string
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return fmt.Errorf(`the "path" of a connector that routed projects is not a string: %w`, err)
	}
	// Before any check of the value: is this the value that is in the file?
	// encoding/json substitutes U+FFFD for malformed UTF-8 and for an
	// unpaired surrogate escape, silently and without an error, so
	// "/work/\ud800" arrives here as "/work/\ufffd" — absolute, clean, and
	// nothing the old writer could have produced (Copilot on #765).
	//
	// This is the second time on this branch that the decoder handed this
	// check a different value from the one on disk. The first was
	// case-folding a key looked up exactly; this is rewriting bytes
	// validated afterwards. Both times the validation was right about the
	// value it saw, and the value it saw was not the one in the file.
	if lossyJSONString(bytes.TrimSpace(raw)) {
		return errors.New(`the "path" of a connector that routed projects is not encoded the way it was written: it holds malformed UTF-8 or an unpaired surrogate escape, which encoding/json silently replaces with U+FFFD, so the value read back is not the value in the file`)
	}
	// POSIX, not this host's rules. The connector runs on Linux only, so the
	// writer of this key wrote a POSIX path — and path.IsAbs answers for
	// that format on every platform, where filepath.IsAbs answers for
	// whatever the reader happens to be compiled for. This package builds
	// everywhere, so with filepath a legitimate Linux-written connect.json
	// would be refused on Windows: the check would reject exactly the files
	// it exists to accept (Copilot on #765).
	//
	// A compatibility check validates what the old writer could produce, not
	// what this reader would accept. Those are the same thing only when the
	// format and the host agree, and a path is where they do not.
	if strings.ContainsRune(legacy, 0) {
		// A NUL is the one byte a filesystem path cannot hold: a path
		// reaches the kernel as a NUL-terminated string, so os.Stat on this
		// value is "invalid argument" and no connector could have written
		// it. path.IsAbs and path.Clean are both content-blind and take it
		// happily (Copilot on #765).
		//
		// Only a NUL. A Linux path may hold a newline, a tab or an escape,
		// and those are bytes a connector really could have written —
		// refusing them would be new strictness rather than a restored
		// check, which is a mistake this branch has already been shown once.
		return errors.New(`the "path" of a connector that routed projects contains a NUL, written \u0000 in JSON, which no filesystem path can hold`)
	}
	if legacy == "" || !path.IsAbs(legacy) || path.Clean(legacy) != legacy {
		return fmt.Errorf(`the "path" of a connector that routed projects is %q, which is not the clean absolute POSIX path such a connector wrote`, legacy)
	}
	return nil
}

// Policy is what admission reads from connect.json, plus the two facts that
// never come from that file: the agent's own Person id (from the profile's
// verified identity) and the --project scope.
//
// connect.json as a whole is written by `basecamp connect setup` (plan step
// 16), which also carries the driver, concurrency and deadline;
// this is only the part admission decides from.
type Policy struct {
	// AgentID is the agent's own Person id. Required.
	AgentID int64 `json:"-"`
	// Buckets is the --project scope. Empty means every project the agent can
	// see.
	Buckets []int64 `json:"-"`

	Trust Trust `json:"trust"`
	// Projects maps a bucket id to the entry for the project it serves.
	Projects map[int64]Project `json:"projects,omitempty"`
	// ProjectsUnknown says the served projects could not be read at all —
	// connect.json is unreadable, or now names another agent. It is not the
	// same as serving none, and the difference is what a person is told: a
	// record whose answer turns on the list is held as a configuration
	// error, rather than answered with a claim about the project that is
	// false while the file is broken.
	ProjectsUnknown bool `json:"-"`
}

// ParsePolicy decodes the admission part of connect.json. Unknown keys are
// ignored: the file carries settings that belong to other steps, and this
// reader has no business refusing a file over the driver's half of it. The
// caller sets AgentID and Buckets, then calls Validate.
//
// Noncanonical and repeated keys are refused, which is not the same
// laxness. An unknown key cannot reach anything this decides from; a key
// spelled "Trust" or "PATH" reaches exactly that, because encoding/json
// matches it. Ignoring one and refusing the other is the difference between
// tolerating a setting that is not ours and accepting an authorization we
// cannot see. See CheckCanonicalKeys.
func ParsePolicy(data []byte) (Policy, error) {
	if err := CheckCanonicalKeys(data); err != nil {
		return Policy{}, fmt.Errorf("admission: parse connect.json: %w", err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("admission: parse connect.json: %w", err)
	}
	if p.Trust.Mode == "" {
		p.Trust.Mode = TrustOperator
	}
	return p, nil
}

// Validate refuses a policy under which trust could be misread. Every check
// here fails closed: a policy that does not validate admits nothing.
func (p Policy) Validate() error {
	switch {
	case p.AgentID <= 0:
		return errors.New("admission: policy needs the agent's Person id")
	case p.Trust.OperatorID <= 0:
		return errors.New("admission: policy needs the operator's Person id")
	case p.Trust.OperatorID == p.AgentID:
		return errors.New("admission: the operator cannot be the agent itself")
	}
	switch p.Trust.Mode {
	case TrustOperator, TrustProject:
		if len(p.Trust.AllowlistIDs) > 0 {
			return fmt.Errorf("admission: allowlist_ids is set but trust mode is %q", p.Trust.Mode)
		}
	case TrustAllowlist:
		// The agent in its own allowlist is a configuration that reads as
		// "the agent may drive itself". The gate refuses the agent regardless;
		// the policy says so here too, where the operator can see it.
		for _, id := range p.Trust.AllowlistIDs {
			if id <= 0 {
				return fmt.Errorf("admission: allowlist id %d is not a Person id", id)
			}
			if id == p.AgentID {
				return errors.New("admission: the agent's own Person id is in the allowlist")
			}
		}
	default:
		return fmt.Errorf("admission: unknown trust mode %q", p.Trust.Mode)
	}
	for bucket := range p.Projects {
		if bucket <= 0 {
			return fmt.Errorf("admission: served project %d: not a bucket id", bucket)
		}
	}
	return nil
}

// inScope reports whether bucket is in the --project scope.
func (p Policy) inScope(bucket int64) bool {
	return len(p.Buckets) == 0 || slices.Contains(p.Buckets, bucket)
}

// served returns the bucket's entry, if connect.json serves the project.
func (p Policy) served(bucket int64) (Project, bool) {
	r, ok := p.Projects[bucket]
	return r, ok
}

// lossyJSONString reports whether encoding/json had to alter raw, a JSON
// string token, to decode it — which it does by substituting U+FFFD, with no
// error, for exactly two things: bytes that are not valid UTF-8, and a
// \uD800-\uDFFF escape that is not half of a well-formed pair. Those are the
// whole of what it rewrites in a string, so testing for them is a test of
// losslessness and not a list of characters to dislike.
//
// Comparing raw against json.Marshal of the decoded value is the tempting
// general form, and it is wrong here. json.Marshal HTML-escapes & < and >,
// so it spells /work/r&d as "/work/r&d": a hand-edited connect.json
// with a literal ampersand — an ordinary thing in a directory name — would
// fail a canonical comparison and take the whole file down with it, over a
// field this code exists to discard. Whether the canonical form even has the
// escape depends on the encoder's SetEscapeHTML, which makes "canonical" two
// different answers. Refusing a legitimate path is the new-strictness
// mistake this branch has already made once; the lossy encoding is the
// actual complaint, so that is what this asks about.
//
// A path that genuinely contains U+FFFD still parses, written either as its
// literal UTF-8 bytes or as �, because neither is something the decoder
// had to replace.
func lossyJSONString(raw []byte) bool {
	if !utf8.Valid(raw) {
		return true
	}
	for i := 0; i < len(raw); {
		if raw[i] != '\\' {
			i++
			continue
		}
		if i+1 >= len(raw) {
			// Malformed, and not this check's to report: the decode above
			// already refused it.
			return false
		}
		if raw[i+1] != 'u' {
			i += 2 // \" \\ \/ \b \f \n \r \t: two bytes, neither a surrogate.
			continue
		}
		r, ok := hex4(raw[i+2:])
		if !ok {
			return false
		}
		i += 6
		if r < 0xD800 || r > 0xDFFF {
			continue
		}
		if r >= 0xDC00 {
			return true // a low surrogate with no high half before it
		}
		if i+5 < len(raw) && raw[i] == '\\' && raw[i+1] == 'u' {
			if lo, ok := hex4(raw[i+2:]); ok && lo >= 0xDC00 && lo <= 0xDFFF {
				i += 6 // a well-formed pair
				continue
			}
		}
		return true // a high surrogate its low half never follows
	}
	return false
}

// hex4 reads the four hex digits of a \u escape. Decoded by hand rather than
// through strconv: JSON's escape grammar is exactly four hex digits, where
// ParseUint would take spellings that grammar does not, and four digits
// cannot exceed 0xFFFF so there is no width to lose.
func hex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, false
		}
		r = r<<4 | d
	}
	return r, true
}
