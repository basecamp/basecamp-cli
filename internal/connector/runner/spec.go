package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Spec names the coding agent a session runs on: the adapter that drives its
// CLI, and the model and reasoning effort handed to it. An empty model or
// effort leaves that choice to the CLI's own configuration.
type Spec struct {
	Adapter string `json:"adapter,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
}

// String is the spec as a person reads it: "codex · gpt-6 · high".
func (s Spec) String() string {
	parts := []string{s.Adapter}
	if s.Model != "" {
		parts = append(parts, s.Model)
	}
	if s.Effort != "" {
		parts = append(parts, s.Effort)
	}
	return strings.Join(parts, " · ")
}

// Over lays s on base: what s names wins. A model belongs to its adapter, so
// a change of adapter does not carry base's model across; effort levels read
// the same everywhere, so base's effort stays unless s names one.
func (s Spec) Over(base Spec) Spec {
	out := base
	if s.Adapter != "" && s.Adapter != base.Adapter {
		out = Spec{Adapter: s.Adapter, Effort: base.Effort}
	}
	if s.Model != "" {
		out.Model = s.Model
	}
	if s.Effort != "" {
		out.Effort = s.Effort
	}
	return out
}

var (
	// A model or effort becomes one argument of a command. Neither may start
	// with a dash, where the CLI would read it as a flag of its own.
	modelPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,127}$`)
	effortPattern = regexp.MustCompile(`^[a-z]{1,16}$`)
)

// Validate refuses a spec no adapter could run. An empty adapter is refused
// unless partial, which a rule's spec is: it names only what it changes.
func (s Spec) Validate(partial bool) error {
	switch {
	case s.Adapter == "" && !partial:
		return errors.New("no adapter is named")
	case s.Adapter != "" && adapterByName(s.Adapter) == nil:
		return fmt.Errorf("unknown adapter %q: expected one of %s", s.Adapter, strings.Join(AdapterNames(), ", "))
	case s.Model != "" && !modelPattern.MatchString(s.Model):
		return fmt.Errorf("model %q is not a model name", s.Model)
	case s.Effort != "" && !effortPattern.MatchString(s.Effort):
		return fmt.Errorf("effort %q is not an effort level", s.Effort)
	}
	return nil
}

// Permissions is what the agents the connector starts may do on this
// computer. It is one switch for every coding agent, and each adapter maps
// it onto its own CLI.
type Permissions string

const (
	// PermissionsFull lets an agent run any command and change any file, as
	// the person, without asking: each CLI is started with its permission
	// prompts off. It is the default, because a headless turn has nobody to
	// answer a prompt.
	PermissionsFull Permissions = "full"
	// PermissionsGuarded starts each CLI under its own unattended
	// guardrails: Claude Code's auto mode, which judges each action and
	// stops the ones it finds unsafe, and Codex's sandbox, which lets it
	// write only in the folder it works in. A CLI with neither cannot be
	// started under it (Adapter.Guarded).
	PermissionsGuarded Permissions = "guarded"
)

// ParsePermissions reads the switch as flags and runner.json spell it.
func ParsePermissions(value string) (Permissions, error) {
	switch mode := Permissions(value); mode {
	case PermissionsFull, PermissionsGuarded:
		return mode, nil
	}
	return "", fmt.Errorf("permissions must be %q or %q, not %q", PermissionsFull, PermissionsGuarded, value)
}

// ConfigFile is the routing file's name, beside connect.json in the
// profile's directory.
const ConfigFile = "runner.json"

// Config is runner.json: which coding agent does what. It is the person's own
// file, written by hand, and read again for every worker, so an edit applies
// to the next request without a restart.
//
// Nothing in it is required. Orchestrator, Worker, Permissions and
// MaxWorkers are what the connector was last started with, kept so the next
// start need not name them again (SaveStart); a start that names one wins
// over what is kept.
// Rules then choose a worker by where the request came from and which repo
// its work goes in.
type Config struct {
	Orchestrator Spec `json:"orchestrator,omitzero"`
	Worker       Spec `json:"worker,omitzero"`
	// Permissions is what agents may do on this computer: "full" or
	// "guarded". Empty means full. It is read again for every turn.
	Permissions Permissions `json:"permissions,omitempty"`
	// MaxWorkers caps how many workers run at once: a request past it waits
	// its turn. Zero, or leaving it out, is no cap. Unlike the rules and
	// the permissions it is read once, when the connector starts.
	MaxWorkers int `json:"max_workers,omitempty"`
	// Rules are tried in order and the first that matches wins. What its
	// worker leaves out comes from the default worker.
	Rules []Rule `json:"rules,omitempty"`
}

// Rule gives requests that match a worker of their own. Every condition a
// rule names must hold; a rule that names none is refused.
type Rule struct {
	// Repo is the local repo the orchestrator chose, or a folder above it.
	// A leading ~ is the person's home.
	Repo string `json:"repo,omitempty"`
	// Project is the Basecamp project the request was made in.
	Project int64 `json:"project,omitempty"`
	// CardTable, Todolist and Within match a request made on, or anywhere
	// under, the recording with that id: a card in that card table, a to-do
	// in that list, a comment on either. Within takes any recording's id.
	CardTable int64 `json:"card_table,omitempty"`
	Todolist  int64 `json:"todolist,omitempty"`
	Within    int64 `json:"within,omitempty"`
	// Ping matches a request made in a Ping, which is in no project.
	Ping bool `json:"ping,omitempty"`

	Worker Spec `json:"worker"`
}

// containers are the recordings the rule asks the request to sit under.
func (r Rule) containers() []int64 {
	var ids []int64
	for _, id := range []int64{r.CardTable, r.Todolist, r.Within} {
		if id != 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

func (r Rule) validate() error {
	if r.Repo == "" && r.Project == 0 && len(r.containers()) == 0 && !r.Ping {
		return errors.New("it names no condition (repo, project, card_table, todolist, within or ping)")
	}
	if r.Repo != "" && r.Repo != "~" && !strings.HasPrefix(r.Repo, "~/") && !filepath.IsAbs(r.Repo) {
		return fmt.Errorf("repo %q is not an absolute path: a request's repo always is, so the rule would never match (use a full path, or one starting with ~/)", r.Repo)
	}
	if r.Project < 0 || slices.ContainsFunc(r.containers(), func(id int64) bool { return id < 0 }) {
		return errors.New("an id is negative")
	}
	if r.Ping && (r.Project != 0 || len(r.containers()) > 0) {
		return errors.New("a Ping is in no project, so ping cannot be combined with project, card_table, todolist or within")
	}
	if r.Worker == (Spec{}) {
		return errors.New("its worker names nothing to change")
	}
	return r.Worker.Validate(true)
}

// describe is the rule's conditions as the log and `dispatch` say them.
func (r Rule) describe() string {
	var parts []string
	if r.Repo != "" {
		parts = append(parts, "repo "+r.Repo)
	}
	if r.Project != 0 {
		parts = append(parts, fmt.Sprintf("project %d", r.Project))
	}
	if r.CardTable != 0 {
		parts = append(parts, fmt.Sprintf("card table %d", r.CardTable))
	}
	if r.Todolist != 0 {
		parts = append(parts, fmt.Sprintf("to-do list %d", r.Todolist))
	}
	if r.Within != 0 {
		parts = append(parts, fmt.Sprintf("within %d", r.Within))
	}
	if r.Ping {
		parts = append(parts, "a Ping")
	}
	return strings.Join(parts, ", ")
}

// maxConfigBytes bounds runner.json: it is a handful of rules.
const maxConfigBytes = 256 << 10

// LoadConfig reads runner.json. A missing file is the empty Config and no
// error: the file is optional. One that cannot be read as written is an
// error, never a guess: a typo in a key would otherwise run a request on the
// wrong agent in silence.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the profile's own directory
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return Config{}, err
	}
	if len(data) > maxConfigBytes {
		return Config{}, fmt.Errorf("%s is larger than %d bytes", path, maxConfigBytes)
	}
	return ParseConfig(data)
}

// ParseConfig reads runner.json's bytes and refuses what it would misread.
func ParseConfig(data []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	if dec.More() {
		return Config{}, fmt.Errorf("%s: more than one JSON value", ConfigFile)
	}
	if err := c.Orchestrator.Validate(true); err != nil {
		return Config{}, fmt.Errorf("%s: orchestrator: %w", ConfigFile, err)
	}
	if err := c.Worker.Validate(true); err != nil {
		return Config{}, fmt.Errorf("%s: worker: %w", ConfigFile, err)
	}
	if c.Permissions != "" {
		if _, err := ParsePermissions(string(c.Permissions)); err != nil {
			return Config{}, fmt.Errorf("%s: %w", ConfigFile, err)
		}
	}
	if err := CheckMaxWorkers(c.MaxWorkers); err != nil {
		return Config{}, fmt.Errorf("%s: max_workers: %w", ConfigFile, err)
	}
	for i, rule := range c.Rules {
		if err := rule.validate(); err != nil {
			return Config{}, fmt.Errorf("%s: rule %d: %w", ConfigFile, i+1, err)
		}
	}
	return c, nil
}

// CheckMaxWorkers refuses a cap on workers that is no cap at all: the most
// that run at once, or zero for none.
func CheckMaxWorkers(limit int) error {
	if limit < 0 {
		return fmt.Errorf("it cannot be negative: it is the most workers that run at once, and 0 is no cap (not %d)", limit)
	}
	return nil
}

// Mode is the permissions in force: what the file says, or full when it says
// nothing.
func (c Config) Mode() Permissions {
	if c.Permissions == "" {
		return PermissionsFull
	}
	return c.Permissions
}

// SaveStart keeps what a connector was started with in runner.json, beside
// whatever else the file holds, and creates the file when there is none. A
// nil maxWorkers is a start that named no cap, and leaves the file's own. A
// file that cannot be read as written is left alone and reported: rewriting
// it would drop whatever the person meant by it.
func SaveStart(path string, orchestrator, worker Spec, mode Permissions, maxWorkers *int) error {
	config, err := LoadConfig(path)
	if err != nil {
		return err
	}
	config.Orchestrator, config.Worker, config.Permissions = orchestrator, worker, mode
	if maxWorkers != nil {
		if err := CheckMaxWorkers(*maxWorkers); err != nil {
			return fmt.Errorf("max_workers: %w", err)
		}
		config.MaxWorkers = *maxWorkers
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+ConfigFile+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Route is what a rule is matched against: where a request came from and
// where its work goes.
type Route struct {
	// Repo is the directory the worker starts in, absolute; empty with none.
	Repo string
	// Project is the request's project; zero for a Ping or with no request.
	Project int64
	Ping    bool
	// Recordings are the request's recording and everything above it.
	Recordings []int64
}

// NeedsRecordings reports whether any rule asks what a request sits under,
// which costs reads of Basecamp to answer.
func (c Config) NeedsRecordings() bool {
	return slices.ContainsFunc(c.Rules, func(r Rule) bool { return len(r.containers()) > 0 })
}

// WorkerFor is the worker a request runs on: the first rule that matches
// route, laid over base, and which rule that was. With no match it is base.
func (c Config) WorkerFor(base Spec, route Route, home string) (Spec, string) {
	for i, rule := range c.Rules {
		if rule.matches(route, home) {
			return rule.Worker.Over(base), fmt.Sprintf("rule %d (%s)", i+1, rule.describe())
		}
	}
	return base, ""
}

func (r Rule) matches(route Route, home string) bool {
	if r.Repo != "" && !underDir(route.Repo, expandHome(r.Repo, home)) {
		return false
	}
	if r.Project != 0 && (route.Ping || r.Project != route.Project) {
		return false
	}
	if r.Ping && !route.Ping {
		return false
	}
	for _, id := range r.containers() {
		if !slices.Contains(route.Recordings, id) {
			return false
		}
	}
	return true
}

// expandHome reads a leading ~ as home.
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok && home != "" {
		return filepath.Join(home, rest)
	}
	return path
}

// underDir reports whether path is dir or inside it. Both are compared as
// written, cleaned: a rule names the repo the way the person knows it.
func underDir(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, strings.TrimSuffix(dir, string(filepath.Separator))+string(filepath.Separator))
}
