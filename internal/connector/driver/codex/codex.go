// Package codex is the spawn driver for Codex: `codex exec --json`, adapted
// onto the driver package's ACP-shaped session.
//
// One process is one turn. `codex exec` reads its prompt from stdin to the end
// and exits once the turn is over, so a session takes a single prompt and
// advertises no follow-up prompts; a follow-up waits for a new attempt, and
// LoadSession continues the conversation in a new process with
// `codex exec resume`.
//
// # Invariants
//
// The driver package's invariants hold here, each by a test in codex_test.go:
//
//  1. Nothing is inherited. The process environment is SessionConfig.Env and
//     the few variables Codex itself needs; the host's config.toml, rules,
//     hooks, plugins, connected apps and skills are not loaded; the only MCP
//     servers are SessionConfig.MCPServers. The model's shell gets Codex's
//     core environment only.
//  2. No secret in argv, none in Codex's environment. An MCP server's
//     environment (a task token among it) is written owner-only and
//     exclusively into the private directory, sourced by the server's own
//     wrapper, which deletes it before it starts the server; Close deletes
//     it again. Codex's own mcp_servers env_vars would hand the token to
//     Codex, and from there to every shell command the model runs.
//  3. The permission mode is set by flags and verified. `codex exec` echoes
//     no mode, and an override Codex does not recognize is silently ignored,
//     so the driver reads the policy Codex actually applied from the turn's
//     turn_context record in its rollout file, and ends the session as
//     unsafe (ErrUnsafeMode) when it is not the one asked for or cannot be
//     read. Both the sandbox mode and the filesystem policy the sandbox is
//     built from are checked: nothing but the working directory writable.
//     A turn is never reported finished before that check passed, and
//     a turn that fails or loses its process after Codex reported its thread
//     waits for the check too, so an unsafe session is reported as unsafe.
//     The check runs beside the turn, not before it: Codex writes the record
//     as the turn starts, so the window is the first model response.
//  4. Every MCP server is required: Codex refuses to start a turn when one
//     fails to initialize, so a worker never runs without its Basecamp
//     server.
//  5. Cancel ends the process group the driver started. A turn ends as
//     TurnCanceled only when Cancel asked for it.
//  6. Updates carry kinds, ids and counts, never the agent's text, a
//     command, or a tool's arguments.
//
// Codex's reach differs from Claude Code's, and this driver claims nothing
// beyond it: Codex reads and searches through shell commands, so its shell
// is not removed but confined by Codex's own sandbox (workspace-write:
// writes only inside the working directory, no network, no /tmp) with
// approvals set to never, so whatever the sandbox would refuse is refused
// without asking anyone. That is still policy, not containment: the sandbox
// is Codex's, not the connector's. One consequence is worth knowing: a
// worktree's git data lives outside the working directory, so a Codex worker
// cannot commit, and a Codex task that edits anything ends with its worktree
// kept. Codex's sandbox reads the whole
// filesystem, so a model in one session can read what the connector's state
// directory holds while it is there, another session's MCP environment file
// between its writing and its server's start among it.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// Name is the driver's name.
const Name = "codex"

// Env is what Codex may take from the connector's environment besides
// driver.BaseEnv: where its state and login live, and an API key for a login
// that uses one.
var Env = []string{"CODEX_HOME", "CODEX_API_KEY"}

// DefaultVerifyTimeout is how long the driver waits for Codex's rollout to
// show the policy it applied.
const DefaultVerifyTimeout = 15 * time.Second

// Options configures the driver.
type Options struct {
	// Binary is the codex executable; "codex" on PATH when empty.
	Binary string
	// Model is passed as --model when set.
	Model string
	// Lookup reads the connector's environment for Env; os.LookupEnv when
	// nil.
	Lookup func(string) (string, bool)
	// CloseGrace is how long a session's process group has between SIGTERM
	// and SIGKILL.
	CloseGrace time.Duration
	// VerifyTimeout bounds the wait for the rollout's policy record.
	VerifyTimeout time.Duration
}

// Driver starts Codex sessions.
type Driver struct {
	opts Options
}

var _ driver.Driver = (*Driver)(nil)

// New builds the driver.
func New(opts Options) *Driver {
	if opts.Binary == "" {
		opts.Binary = "codex"
	}
	if opts.Lookup == nil {
		opts.Lookup = os.LookupEnv
	}
	if opts.CloseGrace <= 0 {
		opts.CloseGrace = 5 * time.Second
	}
	if opts.VerifyTimeout <= 0 {
		opts.VerifyTimeout = DefaultVerifyTimeout
	}
	return &Driver{opts: opts}
}

// Name implements driver.Driver.
func (d *Driver) Name() string { return Name }

// Capabilities implements driver.Driver. A Codex process takes one prompt.
func (d *Driver) Capabilities() driver.Capabilities {
	return driver.Capabilities{LoadSession: true}
}

// NewSession implements driver.Driver. The session's id is Codex's thread id,
// which Codex reports only once the prompt is written: ID is empty until then.
func (d *Driver) NewSession(ctx context.Context, cfg driver.SessionConfig) (driver.Session, error) {
	return d.start(ctx, cfg, "")
}

// LoadSession implements driver.Driver: `codex exec resume <thread id>`.
func (d *Driver) LoadSession(ctx context.Context, cfg driver.SessionConfig, sessionID string) (driver.Session, error) {
	if !validThreadID(sessionID) {
		return nil, fmt.Errorf("%w: session id %q is not a Codex thread id", driver.ErrNotStarted, sessionID)
	}
	return d.start(ctx, cfg, sessionID)
}

// Policy Codex runs every session under, as its turn_context spells it.
const (
	approvalNever  = "never"
	sandboxWorkdir = "workspace-write"
)

// disabledFeatures are Codex features that reach past the session's MCP
// servers and working directory: the account's connected apps and plugins,
// the host's hooks, a browser and the desktop, image generation, memories
// shared across sessions, and installing what a skill asks for.
var disabledFeatures = []string{
	"apps", "plugins", "remote_plugin", "hooks",
	"browser_use", "browser_use_external", "computer_use", "in_app_browser",
	"image_generation", "memories", "skill_mcp_dependency_install", "tool_suggest",
}

// allowedKinds are the tool kinds a policy may allow that Codex can honor:
// its reads, searches and planning run inside the sandbox that confines
// edits to the working directory.
var allowedKinds = []driver.ToolKind{driver.ToolRead, driver.ToolSearch, driver.ToolThink}

var validServerName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// mcpWrapper is the script each MCP server runs under: source the private
// environment file named by $0, delete it, and exec the server. A file that
// cannot be sourced stops the server before it starts, and Codex, which
// requires the server, refuses the turn.
//
//nolint:gosec // G101: a shell script, not a credential
const mcpWrapper = `set -a && . "$0" && set +a && rm -f -- "$0" && exec "$@"`

// Args is the command line for a session, without the binary. envFiles maps
// each MCP server's name to its private environment file. Exposed so the
// flags that hold the policy are tested as written.
func Args(cfg driver.SessionConfig, resumeID string, envFiles map[string]string, model string) ([]string, error) {
	if cfg.Policy == nil {
		return nil, errors.New("codex: a session needs a policy")
	}
	rules := cfg.Policy.Rules()
	if rules.Mode != driver.ModeEditsInWorkDir {
		return nil, fmt.Errorf("%w: codex: no Codex sandbox for policy mode %q", driver.ErrUnusable, rules.Mode)
	}
	if filepath.Clean(rules.WorkDir) != filepath.Clean(cfg.Cwd) {
		return nil, fmt.Errorf("%w: codex: the policy's working directory %q is not the session's %q", driver.ErrUnusable, rules.WorkDir, cfg.Cwd)
	}
	for _, kind := range rules.AllowKinds {
		if !slices.Contains(allowedKinds, kind) {
			return nil, fmt.Errorf("%w: codex: no Codex policy allows kind %q and nothing else", driver.ErrUnusable, kind)
		}
	}

	args := []string{"exec"}
	if resumeID != "" {
		args = append(args, "resume")
	}
	args = append(args,
		"--json",
		// A -c key Codex does not know is ignored in silence, and the flags
		// below are what invariant 1 rests on.
		"--strict-config",
		// The host's config.toml (its MCP servers, profiles, hooks, trust)
		// and its execpolicy rules are not this session's.
		"--ignore-user-config",
		"--ignore-rules",
		// connect.json approved the directory; Codex's own trust prompt has
		// nobody to answer it.
		"--skip-git-repo-check",
		"-c", "approval_policy="+tomlString(approvalNever),
		"-c", "sandbox_mode="+tomlString(sandboxWorkdir),
		"-c", "sandbox_workspace_write.network_access=false",
		"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
		"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
		"-c", "sandbox_workspace_write.writable_roots=[]",
		// The model's shell commands get Codex's core variables, not the
		// worker's whole environment.
		"-c", "shell_environment_policy.inherit="+tomlString("core"),
		"-c", "web_search="+tomlString("disabled"),
		// Skills on the host (the connector's own front-thread skill among
		// them) are not instructions this worker follows.
		"-c", "skills.bundled.enabled=false",
		"-c", "skills.include_instructions=false",
	)
	for _, f := range disabledFeatures {
		args = append(args, "--disable", f)
	}
	for _, s := range cfg.MCPServers {
		if !validServerName.MatchString(s.Name) {
			return nil, fmt.Errorf("%w: codex: MCP server name %q is not one Codex's config can key", driver.ErrUnusable, s.Name)
		}
		if s.Command == "" {
			return nil, fmt.Errorf("%w: codex: MCP server %q has no command", driver.ErrUnusable, s.Name)
		}
		file, ok := envFiles[s.Name]
		if !ok || !filepath.IsAbs(file) {
			return nil, fmt.Errorf("codex: MCP server %q has no private environment file", s.Name)
		}
		approval := "prompt"
		if slices.Contains(rules.AllowMCPServers, s.Name) {
			approval = "approve"
		}
		key := "mcp_servers." + s.Name + "."
		wrapped := append([]string{"-c", mcpWrapper, file, s.Command}, s.Args...)
		args = append(args,
			"-c", key+"command="+tomlString("/bin/sh"),
			"-c", key+"args="+tomlArray(wrapped),
			"-c", key+"required=true",
			"-c", key+"default_tools_approval_mode="+tomlString(approval),
		)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if resumeID != "" {
		args = append(args, resumeID)
	}
	// The prompt is read from stdin, never argv.
	return append(args, "-"), nil
}

func (d *Driver) start(ctx context.Context, cfg driver.SessionConfig, resumeID string) (driver.Session, error) {
	if cfg.Policy == nil || cfg.PrivateDir == "" || cfg.Cwd == "" {
		return nil, fmt.Errorf("%w: a session needs a policy, a working directory and a private directory", driver.ErrNotStarted)
	}
	env := mergeEnv(cfg.Env, driver.BuildEnv(Env, d.opts.Lookup, nil))
	sessions, err := sessionsDir(env)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", driver.ErrNotStarted, err)
	}
	var offset int64
	if resumeID != "" {
		path, err := findRollout(sessions, resumeID)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", driver.ErrNotStarted, err)
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", driver.ErrNotStarted, err)
		}
		offset = info.Size()
	}
	envFiles, err := writeEnvFiles(cfg.PrivateDir, cfg.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", driver.ErrNotStarted, err)
	}
	removeFiles := func() {
		for _, f := range envFiles {
			_ = os.Remove(f)
		}
	}
	args, err := Args(cfg, resumeID, envFiles, d.opts.Model)
	if err != nil {
		removeFiles()
		return nil, fmt.Errorf("%w: %w", driver.ErrNotStarted, err)
	}
	worker, err := driver.StartWorker(ctx, cfg.Launcher, cfg.Scope, driver.Command{Path: d.opts.Binary, Args: args, Env: env, Dir: cfg.Cwd})
	if err != nil {
		removeFiles()
		return nil, err
	}
	s := &session{
		id:          resumeID,
		worker:      worker,
		cwd:         cfg.Cwd,
		sessions:    sessions,
		offset:      offset,
		envFiles:    envFiles,
		grace:       d.opts.CloseGrace,
		verifyAfter: d.opts.VerifyTimeout,
		updates:     make(chan driver.Update, 256),
		readerEnd:   make(chan struct{}),
	}
	go s.read()
	return s, nil
}

// sessionsDir is where Codex writes rollouts for this environment.
func sessionsDir(env []string) (string, error) {
	vars := driver.EnvMap(env)
	home := vars["CODEX_HOME"]
	if home == "" {
		if vars["HOME"] == "" {
			return "", errors.New("codex: the worker's environment names no HOME or CODEX_HOME")
		}
		home = filepath.Join(vars["HOME"], ".codex")
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("codex: CODEX_HOME %q is not absolute", home)
	}
	return filepath.Join(home, "sessions"), nil
}

// mergeEnv adds the driver's own variables to the dispatcher's allowlisted
// environment. A variable the dispatcher set wins.
func mergeEnv(base, extra []string) []string {
	have := map[string]bool{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		have[k] = true
	}
	out := slices.Clone(base)
	if out == nil {
		out = []string{}
	}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		if !have[k] {
			out = append(out, kv)
		}
	}
	slices.Sort(out)
	return out
}

var validEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// writeEnvFiles writes each MCP server's environment owner-only and
// exclusively into dir, as shell assignments the wrapper sources.
func writeEnvFiles(dir string, servers []driver.MCPServer) (map[string]string, error) {
	files := map[string]string{}
	fail := func(err error) (map[string]string, error) {
		for _, f := range files {
			_ = os.Remove(f)
		}
		return nil, err
	}
	for _, s := range servers {
		if !validServerName.MatchString(s.Name) {
			return fail(fmt.Errorf("codex: MCP server name %q is not one Codex's config can key", s.Name))
		}
		if _, dup := files[s.Name]; dup {
			return fail(fmt.Errorf("codex: MCP server %q is named twice", s.Name))
		}
		names := make([]string, 0, len(s.Env))
		for k := range s.Env {
			names = append(names, k)
		}
		slices.Sort(names)
		var buf bytes.Buffer
		for _, k := range names {
			v := s.Env[k]
			if !validEnvName.MatchString(k) || strings.ContainsRune(v, 0) {
				return fail(fmt.Errorf("codex: MCP server %q has an environment variable a shell cannot carry", s.Name))
			}
			buf.WriteString(k + "=" + shellQuote(v) + "\n")
		}
		path := filepath.Join(dir, "mcp-"+s.Name+".env")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fail(fmt.Errorf("codex: write MCP environment: %w", err))
		}
		files[s.Name] = path
		if _, err := f.Write(buf.Bytes()); err != nil {
			_ = f.Close()
			return fail(fmt.Errorf("codex: write MCP environment: %w", err))
		}
		if err := f.Close(); err != nil {
			return fail(fmt.Errorf("codex: write MCP environment: %w", err))
		}
	}
	return files, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// tomlString is a TOML basic string. Only \\, \" and \uXXXX escapes are
// used, which TOML and JSON read alike.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlArray(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = tomlString(s)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

// session is one Codex process.
type session struct {
	worker      *driver.Worker
	cwd         string
	sessions    string
	offset      int64
	envFiles    map[string]string
	grace       time.Duration
	verifyAfter time.Duration

	updates   chan driver.Update
	readerEnd chan struct{}

	mu       sync.Mutex
	id       string
	prompted bool
	// ended is the reader's record that the worker's output is over.
	ended bool
	// cancelEarly is a Cancel before any prompt: the prompt, when it comes,
	// is not sent.
	cancelEarly bool
	turn        *turn
	verifyDone  chan struct{}
	verifyErr   error
	closed      bool
	writeMu     sync.Mutex
}

// turn is the prompt in flight.
type turn struct {
	done     chan struct{}
	result   driver.PromptResult
	err      error
	canceled bool
	refusals []driver.Refusal
}

var _ driver.Session = (*session)(nil)

func (s *session) ID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.id
}
func (s *session) Process() driver.Process       { return s.worker.Process() }
func (s *session) Updates() <-chan driver.Update { return s.updates }
func (s *session) Done() <-chan struct{}         { return s.worker.Done() }
func (s *session) Exit() driver.Exit             { return s.worker.Exit() }

// errOnePrompt is a second prompt to a Codex process.
var errOnePrompt = fmt.Errorf("%w: a Codex session takes one prompt", driver.ErrSessionEnded)

// Prompt implements driver.Session: the prompt is written to stdin, which is
// then closed, and the turn runs to its end.
func (s *session) Prompt(ctx context.Context, prompt string) (driver.PromptResult, error) {
	s.mu.Lock()
	switch {
	case s.closed:
		s.mu.Unlock()
		return driver.PromptResult{}, driver.ErrSessionEnded
	case s.prompted:
		s.mu.Unlock()
		return driver.PromptResult{}, errOnePrompt
	case s.cancelEarly:
		// Cancel came before the prompt: nothing is written, and the worker
		// is ended.
		s.prompted = true
		s.mu.Unlock()
		go s.worker.Terminate(s.grace)
		return driver.PromptResult{Stop: driver.TurnCanceled}, nil
	}
	s.prompted = true
	t := &turn{done: make(chan struct{})}
	s.turn = t
	s.mu.Unlock()

	s.writeMu.Lock()
	_, err := io.WriteString(s.worker.Stdin(), prompt)
	if closeErr := s.worker.Stdin().Close(); err == nil {
		err = closeErr
	}
	s.writeMu.Unlock()
	if err != nil {
		// A cancel that closed the worker's stdin is what made the write
		// fail: the turn is canceled, not a session that ended on its own.
		s.mu.Lock()
		canceled := t.canceled
		s.mu.Unlock()
		if canceled {
			s.finishCanceled(t, nil)
		} else {
			s.finish(t, driver.PromptResult{}, fmt.Errorf("%w: %w", driver.ErrSessionEnded, err))
		}
	}
	select {
	case <-t.done:
		return t.result, t.err
	case <-ctx.Done():
		return driver.PromptResult{}, ctx.Err()
	}
}

// Cancel implements driver.Session: the process group is ended, and the turn
// in flight ends canceled. A Cancel before the session's prompt cancels that
// prompt, which the dispatcher may send from another goroutine an instant
// later.
func (s *session) Cancel(context.Context) error {
	s.mu.Lock()
	t := s.turn
	if t != nil {
		t.canceled = true
	} else if !s.prompted {
		// A cancel that races the prompt it is meant for.
		s.cancelEarly = true
	}
	s.mu.Unlock()
	if t == nil {
		return nil
	}
	go s.worker.Terminate(s.grace)
	return nil
}

// Close implements driver.Session.
func (s *session) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.writeMu.Lock()
	_ = s.worker.Stdin().Close()
	s.writeMu.Unlock()
	select {
	case <-s.worker.Done():
	case <-time.After(s.grace):
	}
	s.worker.Terminate(s.grace)
	select {
	case <-s.readerEnd:
	case <-time.After(s.grace):
		// The worker is gone and a descendant outside its group still holds
		// the output: stop reading it, rather than hold the attempt, its
		// working directory and the connector's shutdown open forever.
		s.worker.CloseStdout()
		<-s.readerEnd
	}
	for _, f := range s.envFiles {
		_ = os.Remove(f)
	}
	return nil
}

func (s *session) finish(t *turn, result driver.PromptResult, err error) {
	s.mu.Lock()
	if s.turn != t {
		s.mu.Unlock()
		return
	}
	s.turn = nil
	s.mu.Unlock()
	t.result, t.err = result, err
	close(t.done)
}

func (s *session) emit(u driver.Update) {
	u.At = time.Now()
	select {
	case s.updates <- u:
	default:
	}
}

// read maps the process's JSON lines onto updates and the turn's result until
// the process closes its stdout.
func (s *session) read() {
	defer func() {
		// The updates channel closes last: finishing the turn still emits
		// (a refusal read from stderr), and a send on a closed channel is a
		// panic, not a dropped update.
		defer close(s.updates)
		s.mu.Lock()
		t := s.turn
		s.mu.Unlock()
		if t != nil {
			s.mu.Lock()
			canceled := t.canceled
			refusals := slices.Clone(t.refusals)
			s.mu.Unlock()
			switch {
			case canceled:
				s.finishCanceled(t, refusals)
			default:
				s.stderrRefusals()
				refusals = s.refusalsOf(t)
				err := s.failedVerification()
				if err == nil {
					err = driver.ErrSessionEnded
				}
				s.finish(t, driver.PromptResult{Refusals: refusals}, err)
			}
		}
		close(s.readerEnd)
	}()
	scanner := bufio.NewScanner(s.worker.Stdout())
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		s.handle(scanner.Bytes())
	}
	// Drain what a scanner error left, so the process never blocks writing.
	_, _ = io.Copy(io.Discard, s.worker.Stdout())
}

// event is the part of a `codex exec --json` line the driver reads. Text,
// commands, arguments and results are never decoded into anything kept.
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     *struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Status string `json:"status"`
		Server string `json:"server"`
		Tool   string `json:"tool"`
		Text   string `json:"text"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	} `json:"item"`
	Usage *struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

func (s *session) handle(line []byte) {
	var e event
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	switch e.Type {
	case "thread.started":
		s.threadStarted(e.ThreadID)
	case "item.started", "item.updated", "item.completed":
		if e.Item != nil {
			s.item(e.Type, e)
		}
	case "turn.completed":
		s.turnCompleted(e)
	case "turn.failed":
		s.turnFailed()
	}
}

// threadStarted records the thread id and starts reading the rollout for the
// policy Codex applied (invariant 3). An unsafe session is ended as soon as the
// check fails, while the model may still be thinking; a turn that ends first
// waits for the check.
func (s *session) threadStarted(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.verifyDone != nil {
		return
	}
	done := make(chan struct{})
	s.verifyDone = done
	if !validThreadID(id) || (s.id != "" && s.id != id) {
		s.verifyErr = fmt.Errorf("%w: Codex reported thread %q", driver.ErrUnsafeMode, sanitize(id))
		close(done)
		go s.unsafe(s.verifyErr)
		return
	}
	s.id = id
	go func() {
		err := verifyRollout(s.sessions, id, s.offset, s.cwd, s.verifyAfter)
		s.mu.Lock()
		s.verifyErr = err
		s.mu.Unlock()
		close(done)
		if err != nil {
			s.unsafe(err)
		}
	}()
}

// unsafe ends the turn in flight with err and the process group.
func (s *session) unsafe(err error) {
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t != nil {
		s.finish(t, driver.PromptResult{}, err)
	}
	s.worker.Terminate(0)
}

// failedVerification is a turn that ended some other way than completed: once
// Codex reported its thread, the check's verdict is waited for, so an unsafe
// session is reported as unsafe rather than as a plain failure. Before a
// thread there was no turn to verify.
func (s *session) failedVerification() error {
	s.mu.Lock()
	started := s.verifyDone != nil
	s.mu.Unlock()
	if !started {
		return nil
	}
	return s.verified()
}

// finishCanceled ends a turn the connector canceled. A policy check that has
// already failed is reported over the cancel; one still running is not
// waited for, because the process it would judge is being ended by the
// cancel anyway.
func (s *session) finishCanceled(t *turn, refusals []driver.Refusal) {
	s.mu.Lock()
	done := s.verifyDone
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
			s.mu.Lock()
			verdict := s.verifyErr
			s.mu.Unlock()
			if verdict != nil {
				s.finish(t, driver.PromptResult{Refusals: refusals}, verdict)
				return
			}
		default:
		}
	}
	s.finish(t, driver.PromptResult{Stop: driver.TurnCanceled, Refusals: refusals}, nil)
}

// verified waits for the policy check's verdict.
func (s *session) verified() error {
	s.mu.Lock()
	done := s.verifyDone
	s.mu.Unlock()
	if done == nil {
		return fmt.Errorf("%w: the turn ended before Codex reported its thread", driver.ErrUnsafeMode)
	}
	select {
	case <-done:
	case <-time.After(s.verifyAfter + 5*time.Second):
		return fmt.Errorf("%w: the policy check did not finish", driver.ErrUnsafeMode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verifyErr
}

func (s *session) item(kind string, e event) {
	it := e.Item
	u := driver.Update{ToolCallID: it.ID, Status: toolStatus(kind, it.Status)}
	switch it.Type {
	case "agent_message":
		if kind == "item.completed" {
			s.emit(driver.Update{Kind: driver.UpdateAgentMessageChunk, Chars: len(it.Text)})
		}
		return
	case "reasoning", "error", "user_message":
		return
	case "command_execution":
		u.Tool, u.ToolKind = "exec", driver.ToolExecute
	case "file_change":
		u.Tool, u.ToolKind = "apply_patch", driver.ToolEdit
	case "mcp_tool_call":
		u.Tool, u.ToolKind = "mcp__"+sanitize(it.Server)+"__"+sanitize(it.Tool), driver.ToolOther
	case "web_search":
		u.Tool, u.ToolKind = "web_search", driver.ToolFetch
	case "todo_list":
		if kind == "item.completed" || kind == "item.started" {
			s.emit(driver.Update{Kind: driver.UpdatePlan})
		}
		return
	default:
		u.Tool, u.ToolKind = sanitize(it.Type), driver.ToolOther
	}
	if kind == "item.started" {
		u.Kind = driver.UpdateToolCall
	} else {
		u.Kind = driver.UpdateToolCallUpdate
	}
	s.emit(u)
	if kind == "item.completed" && it.Type == "mcp_tool_call" && it.Error != nil && refusedByApproval(it.Error.Message) {
		s.refused(it.ID, u.Tool, u.ToolKind)
	}
}

func toolStatus(kind, status string) driver.ToolStatus {
	switch status {
	case "completed":
		return driver.ToolCompleted
	case "failed", "declined":
		return driver.ToolFailed
	case "in_progress":
		return driver.ToolInProgress
	}
	if kind == "item.started" {
		return driver.ToolInProgress
	}
	return driver.ToolCompleted
}

// refusedByApproval is Codex's message for a call its approval policy
// refused: under approvals set to never, a call that needs one is refused.
func refusedByApproval(message string) bool {
	return strings.Contains(message, "approval policy is never") || strings.Contains(message, "rejected by user approval settings")
}

func (s *session) refused(id, tool string, kind driver.ToolKind) {
	s.mu.Lock()
	if s.turn != nil {
		s.turn.refusals = append(s.turn.refusals, driver.Refusal{ToolCallID: id, Tool: tool})
	}
	s.mu.Unlock()
	s.emit(driver.Update{Kind: driver.UpdatePermission, ToolCallID: id, Tool: tool, ToolKind: kind, Allowed: false})
}

func (s *session) turnCompleted(e event) {
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t == nil {
		return
	}
	if err := s.verified(); err != nil {
		s.finish(t, driver.PromptResult{}, err)
		s.worker.Terminate(0)
		return
	}
	// Codex exits right after the turn it completed, and its stderr is whole
	// only once it has: a refusal it logged and did not put on the stream is
	// in the tail by then.
	select {
	case <-s.worker.Done():
	case <-time.After(s.grace):
	}
	s.stderrRefusals()
	s.mu.Lock()
	result := driver.PromptResult{Stop: driver.TurnEndTurn, Refusals: slices.Clone(t.refusals)}
	if t.canceled {
		// Only a cancel the connector asked for reads as canceled.
		result.Stop = driver.TurnCanceled
	}
	s.mu.Unlock()
	if e.Usage != nil {
		result.Usage = driver.Usage{InputTokens: e.Usage.InputTokens, OutputTokens: e.Usage.OutputTokens}
		s.emit(driver.Update{Kind: driver.UpdateUsage, Usage: &result.Usage})
	}
	s.finish(t, result, nil)
}

func (s *session) turnFailed() {
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t == nil {
		return
	}
	s.mu.Lock()
	canceled := t.canceled
	refusals := slices.Clone(t.refusals)
	s.mu.Unlock()
	if canceled {
		s.finishCanceled(t, refusals)
		return
	}
	s.stderrRefusals()
	refusals = s.refusalsOf(t)
	if err := s.failedVerification(); err != nil {
		s.finish(t, driver.PromptResult{Refusals: refusals}, err)
		s.worker.Terminate(0)
		return
	}
	s.finish(t, driver.PromptResult{Refusals: refusals}, errors.New("codex: the turn failed"))
}

// refusalsOf is a turn's refusals so far.
func (s *session) refusalsOf(t *turn) []driver.Refusal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(t.refusals)
}

// stderrRefusals counts the refusals Codex logs but does not put on its JSON
// stream: an edit outside the working directory. Best effort: the stderr
// kept is a tail.
func (s *session) stderrRefusals() {
	tail := s.worker.StderrTail()
	for line := range strings.SplitSeq(tail, "\n") {
		if !refusedByApproval(line) {
			continue
		}
		tool, kind := "exec", driver.ToolExecute
		if strings.Contains(line, "patch rejected") {
			tool, kind = "apply_patch", driver.ToolEdit
		}
		s.refused("", tool, kind)
	}
}

// turnContext is the part of a rollout's turn_context record the driver
// checks.
type turnContext struct {
	Cwd            string `json:"cwd"`
	ApprovalPolicy string `json:"approval_policy"`
	SandboxPolicy  struct {
		Type                string   `json:"type"`
		NetworkAccess       bool     `json:"network_access"`
		ExcludeTmpdirEnvVar bool     `json:"exclude_tmpdir_env_var"`
		ExcludeSlashTmp     bool     `json:"exclude_slash_tmp"`
		WritableRoots       []string `json:"writable_roots"`
	} `json:"sandbox_policy"`
	// FileSystem is the filesystem policy Codex's sandbox is actually built
	// from; PermissionProfile carries the same in older records.
	FileSystem        *fileSystemPolicy `json:"file_system_sandbox_policy"`
	PermissionProfile *struct {
		FileSystem *fileSystemPolicy `json:"file_system"`
	} `json:"permission_profile"`
}

type fileSystemPolicy struct {
	Kind    string `json:"kind"`
	Type    string `json:"type"`
	Entries []struct {
		Path struct {
			Type string `json:"type"`
			Path string `json:"path"`
		} `json:"path"`
		Access string `json:"access"`
	} `json:"entries"`
}

// writesOnlyIn reports whether a filesystem policy is restricted and lets
// nothing but cwd be written.
func (p *fileSystemPolicy) writesOnlyIn(cwd string) bool {
	if p == nil || (p.Kind != "restricted" && p.Type != "restricted") {
		return false
	}
	writable := false
	for _, e := range p.Entries {
		if e.Access == "read" || e.Access == "none" {
			continue
		}
		if e.Path.Type != "path" || !samePath(e.Path.Path, cwd) {
			return false
		}
		writable = true
	}
	return writable
}

// verifyRollout waits for the first turn_context record after offset in the
// thread's rollout and checks it is the policy the flags asked for.
func verifyRollout(sessions, threadID string, offset int64, cwd string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var path string
	for {
		if path == "" {
			if p, err := findRollout(sessions, threadID); err == nil {
				path = p
			}
		}
		if path != "" {
			tc, found, next, err := readTurnContext(path, offset)
			if err != nil {
				return fmt.Errorf("%w: reading Codex's rollout: %w", driver.ErrUnsafeMode, err)
			}
			offset = next
			if found {
				return checkTurnContext(tc, cwd)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: Codex's rollout showed no policy within %s", driver.ErrUnsafeMode, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func checkTurnContext(tc turnContext, cwd string) error {
	p := tc.SandboxPolicy
	switch {
	case tc.ApprovalPolicy != approvalNever:
		return fmt.Errorf("%w: asked for approvals %q, Codex applied %q", driver.ErrUnsafeMode, approvalNever, sanitize(tc.ApprovalPolicy))
	case p.Type != sandboxWorkdir:
		return fmt.Errorf("%w: asked for sandbox %q, Codex applied %q", driver.ErrUnsafeMode, sandboxWorkdir, sanitize(p.Type))
	case p.NetworkAccess || !p.ExcludeSlashTmp || !p.ExcludeTmpdirEnvVar || len(p.WritableRoots) > 0:
		return fmt.Errorf("%w: Codex's sandbox reaches past the working directory", driver.ErrUnsafeMode)
	case !samePath(tc.Cwd, cwd):
		return fmt.Errorf("%w: Codex runs in another directory than the session's", driver.ErrUnsafeMode)
	}
	fs := tc.FileSystem
	if fs == nil && tc.PermissionProfile != nil {
		fs = tc.PermissionProfile.FileSystem
	}
	if !fs.writesOnlyIn(cwd) {
		return fmt.Errorf("%w: Codex's filesystem sandbox writes past the working directory, or was not reported", driver.ErrUnsafeMode)
	}
	return nil
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// readTurnContext scans complete lines from offset for a turn_context record.
// It returns the offset after the last complete line it read.
func readTurnContext(path string, offset int64) (turnContext, bool, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return turnContext{}, false, offset, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return turnContext{}, false, offset, err
	}
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			// A line without its newline is still being written.
			if errors.Is(err, io.EOF) {
				return turnContext{}, false, offset, nil
			}
			return turnContext{}, false, offset, err
		}
		offset += int64(len(line))
		var rec struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "turn_context" {
			continue
		}
		var tc turnContext
		if err := json.Unmarshal(rec.Payload, &tc); err != nil {
			return turnContext{}, false, offset, errors.New("an unreadable turn_context record")
		}
		return tc, true, offset, nil
	}
}

// findRollout finds a thread's rollout file: sessions/YYYY/MM/DD/rollout-*-<id>.jsonl.
func findRollout(sessions, threadID string) (string, error) {
	if !validThreadID(threadID) {
		return "", fmt.Errorf("codex: %q is not a thread id", sanitize(threadID))
	}
	matches, err := filepath.Glob(filepath.Join(sessions, "*", "*", "*", "rollout-*-"+threadID+".jsonl"))
	if err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("codex: no rollout for thread %s", threadID)
	case 1:
		return matches[0], nil
	}
	return "", fmt.Errorf("codex: %d rollouts for thread %s", len(matches), threadID)
}

var threadIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func validThreadID(s string) bool { return threadIDPattern.MatchString(s) }

// sanitize keeps a vendor token (a server or tool name, a policy value) to a
// short run of plain characters.
func sanitize(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			out = append(out, r)
		}
		if len(out) >= 64 {
			break
		}
	}
	return string(out)
}
