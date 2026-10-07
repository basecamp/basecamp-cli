package admission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// TrustMode names who, besides the operator, may drive the agent.
type TrustMode string

const (
	// TrustOperator trusts the operator alone. The default.
	TrustOperator TrustMode = "operator"
	// TrustAllowlist trusts the operator and a list of Person ids.
	TrustAllowlist TrustMode = "allowlist"
	// TrustProject trusts the operator, anyone in the allowlist, and any
	// non-client member of the event's project — the members as
	// participants.
	TrustProject TrustMode = "project"
)

// Role is whose request an admitted event is. Operators are the people whose
// word authorizes the agent: the operator, and anyone named in the allowlist.
// Participants are the wider set project trust admits — any non-client
// member of the project — whose requests reach the agent but carry no
// authority of their own. Admission says which; what each may get the agent
// to do is the policy of whoever reads the request.
type Role string

const (
	RoleOperator    Role = "operator"
	RoleParticipant Role = "participant"
)

// Trust is the trust section of connect.json.
type Trust struct {
	Mode TrustMode `json:"mode"`
	// OperatorID is the Person id of the person who approved the connection.
	OperatorID int64 `json:"operator_id"`
	// AllowlistIDs are the Person ids trusted as operators besides the
	// operator, in allowlist or project mode.
	AllowlistIDs []int64 `json:"allowlist_ids,omitempty"`
	// AllowAssignments lets the people AllowlistIDs names assign the agent
	// work, as the operator does. Off, assignments are the operator's alone.
	AllowAssignments bool `json:"allow_assignments,omitempty"`

	// AgentIDs are other agents (Agent persons) allowed to wake this one by
	// @mentioning it. Empty, the default, admits no agent at all. An allowed
	// agent reaches the agent by mention only, never by assignment, by a
	// comment on a thread it follows or by a completion, and its request is a
	// participant's, never an operator's. Any trust mode may name them.
	AgentIDs []int64 `json:"agent_ids,omitempty"`
	// AgentThreadCap is how many allowed agents' mentions one conversation
	// (a thread, or a Campfire) admits within AgentCapWindow, across every
	// allowed agent. Zero means DefaultAgentThreadCap.
	AgentThreadCap int `json:"agent_thread_cap,omitempty"`
	// AgentDailyCap is how many of one allowed agent's mentions are admitted
	// within AgentCapWindow across every conversation, so an agent opening a
	// new thread for each reply is bounded too. Zero means
	// DefaultAgentDailyCap.
	AgentDailyCap int `json:"agent_daily_cap,omitempty"`
}

// Agent-to-agent loop bounds. Two agents that allow each other can answer
// each other's mentions indefinitely; these caps are what stops that. They
// count admitted agent requests in the ledger over a rolling window.
const (
	DefaultAgentThreadCap = 3
	DefaultAgentDailyCap  = 20
	// MaxAgentCap bounds either cap, so a cap is never a way to switch the
	// protection off.
	MaxAgentCap    = 100
	AgentCapWindow = 24 * time.Hour
)

// allowsAgent reports whether id is an agent the policy lets mention this one.
func (t Trust) allowsAgent(id int64) bool {
	return id > 0 && slices.Contains(t.AgentIDs, id)
}

// ThreadCap is the per-conversation cap in force: AgentThreadCap, or the
// default when unset.
func (t Trust) ThreadCap() int {
	if t.AgentThreadCap > 0 {
		return t.AgentThreadCap
	}
	return DefaultAgentThreadCap
}

// DailyCap is the per-agent cap in force: AgentDailyCap, or the default when
// unset.
func (t Trust) DailyCap() int {
	if t.AgentDailyCap > 0 {
		return t.AgentDailyCap
	}
	return DefaultAgentDailyCap
}

// roleOf is the role a trusted person holds, before any membership read: an
// operator when the policy names them, otherwise a participant, whom only
// project mode admits and only once membership confirms them.
func (t Trust) roleOf(person int64) Role {
	if person == t.OperatorID || slices.Contains(t.AllowlistIDs, person) {
		return RoleOperator
	}
	return RoleParticipant
}

// least is the lesser of two roles: a request any participant had a hand in
// carries no operator's word.
func least(a, b Role) Role {
	if a == RoleOperator && b == RoleOperator {
		return RoleOperator
	}
	return RoleParticipant
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
	// The entry's own keys, before anything reads one. DisallowUnknownFields
	// below refuses a name this type does not have; it does not refuse a
	// name it does have spelled differently or given twice, because the
	// decoder matches case-insensitively and takes the last of a repeated
	// key. So {"WATCH_COMPLETIONS":true} sets WatchCompletions, and
	// {"class":"a","CLASS":"b"} silently keeps "b" — an entry that does not
	// say what it reads as (Copilot on #765).
	//
	// Here rather than only in the two readers because Project is exported:
	// whoever decodes an entry gets this, including setup's own
	// refuseMalformedProjects, which unmarshals a raw entry into this type
	// precisely to name the project behind a refusal. A component that can
	// only be trusted when its caller did something first is one a later
	// edit breaks without touching it.
	if err := CheckCanonicalKeys(data); err != nil {
		return err
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
// The exact lookup is the complete one, and only because UnmarshalJSON walks
// the entry's keys a few lines above this: encoding/json matches a struct tag
// without regard to case, so {"Path": "../x"} fills LegacyPath while a lookup
// of "path" sees an absent key and validates nothing (Copilot on #765). The
// walk refuses every spelling but this one, so by here there is one.
//
// That precondition is established inside the same function that calls this,
// not in another package's call order, and TestAProjectEntryFailsClosedOnItsOwn
// decodes a bare {"Path":null} to hold it. Scanning case-insensitively here
// as well was tried and taken back out: after the walk the branch cannot run,
// and unreachable code with a comment claiming it is load-bearing is the
// stale description this branch spent a third of its rounds deleting.
func checkLegacyPath(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	raw, ok := probe["path"]
	if !ok {
		return nil
	}
	return checkLegacyPathValue(raw)
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
	// POSIX, not this host's rules. The connector runs on Linux and macOS, so the
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
	// Not made redundant by the one in Project.UnmarshalJSON, which sees a
	// single entry. This walks the whole document, and everything that
	// authorizes outside an entry is only here: "Trust" beside "trust",
	// "Projects" beside "projects", a project id written "01", any of those
	// keys given twice. An entry-level walk cannot see one of them.
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
	case p.Trust.AllowAssignments && !slices.ContainsFunc(p.Trust.AllowlistIDs, func(id int64) bool { return id != p.Trust.OperatorID }):
		// An opt-in nobody can use reads as a widening that is not there:
		// the operator assigns without one.
		return errors.New("admission: allow_assignments is set but the allowlist names nobody besides the operator")
	}
	switch p.Trust.Mode {
	case TrustOperator:
		if len(p.Trust.AllowlistIDs) > 0 {
			return fmt.Errorf("admission: allowlist_ids is set but trust mode is %q", p.Trust.Mode)
		}
	case TrustAllowlist, TrustProject:
		// Named operators, alone or beside the project's members. The agent
		// in its own allowlist is a configuration that reads as "the agent
		// may drive itself". The gate refuses the agent regardless; the
		// policy says so here too, where the operator can see it.
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
	if err := p.validateAgents(); err != nil {
		return err
	}
	for bucket := range p.Projects {
		if bucket <= 0 {
			return fmt.Errorf("admission: served project %d: not a bucket id", bucket)
		}
	}
	return nil
}

// validateAgents refuses an agent list that could be misread: an id that is
// not a Person id, the agent itself, or someone already trusted as a person,
// who would then hold two roles at once. The caps must be within bounds.
func (p Policy) validateAgents() error {
	for _, id := range p.Trust.AgentIDs {
		switch {
		case id <= 0:
			return fmt.Errorf("admission: agent id %d is not a Person id", id)
		case id == p.AgentID:
			return errors.New("admission: the agent's own Person id is in agent_ids; an agent never wakes itself")
		case id == p.Trust.OperatorID || slices.Contains(p.Trust.AllowlistIDs, id):
			return fmt.Errorf("admission: person %d is both a trusted person and an allowed agent", id)
		}
	}
	for name, cap := range map[string]int{"agent_thread_cap": p.Trust.AgentThreadCap, "agent_daily_cap": p.Trust.AgentDailyCap} {
		if cap < 0 || cap > MaxAgentCap {
			return fmt.Errorf("admission: %s %d is outside 1 to %d", name, cap, MaxAgentCap)
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
