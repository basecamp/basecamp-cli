// Package acp is the connector as an Agent Client Protocol v1 client: one
// adapter process per session, spoken to over newline-delimited JSON-RPC 2.0
// on its stdio.
//
// A session is opened with initialize, session/new {cwd, mcpServers} (or
// session/load / session/resume, where the agent advertises them), and put in
// its adapter's asking mode with session/set_mode before anything is prompted.
// Prompts are session/prompt; progress is session/update, reduced to kinds,
// ids and counts; a turn is ended with session/cancel; and every
// session/request_permission is answered by the connector's policy. The client
// advertises no fs and no terminal capability, so the agent works through its
// own tools and asks.
//
// # Invariants
//
// Beyond the driver package's, each held by a test in this package:
//
//  1. The adapter's environment is an allowlist. The adapter process gets
//     SessionConfig.Env plus the variables its Adapter names, by exact name;
//     every MCP server gets exactly its declared MCPServer.Env, sent as
//     mcpServers[].env. Nothing of the connector's own environment is passed
//     by inheritance, so a host token (CLAUDE_CODE_MESSAGING_TOKEN) never
//     reaches the adapter or anything it starts.
//  2. No session runs outside its asking mode. After session/new or
//     session/load the driver sets the adapter's asking mode and reads the
//     mode back (session/set_config_option's configOptions, or a
//     current_mode_update); a session that does not offer the mode, or does
//     not confirm it, is ended with ErrUnsafeMode before NewSession returns.
//     A later report of any other mode ends the session the same way.
//  3. Permission answers are chosen by option kind, never by id or label.
//     An allow is allow_once and never allow_always, so no answer outlives
//     the request; a refusal is reject_once (reject_always when that is all
//     that is offered). A request outside a turn, for another session, or
//     before the mode is confirmed is refused.
//  4. A refusal is the driver's record, not the agent's stop reason. Every
//     refusal of a turn is on its PromptResult; a canceled stop the
//     connector did not ask for is reported as TurnRefusal when the turn had
//     refusals and as an error otherwise, never as TurnCanceled.
//  5. Load is gated by what the agent advertised at initialize: session/load
//     when loadSession is true, session/resume when sessionCapabilities.resume
//     is present, otherwise an error. Its history replay is not progress.
//     6a. A configuration this driver cannot run — an adapter with no asking
//     mode for the policy's, a policy for another directory, an MCP server
//     without an absolute command, a Codex config that declares MCP servers —
//     is ErrUnusable beside ErrNotStarted: nothing started, and a retry would
//     fail the same way.
//  6. The adapter is the pinned one: initialize must report protocol version
//     1 and the Adapter's package and version, or the session is ended.
//  7. Nothing the agent volunteers is kept: _auth/status_update (which
//     carries the account's email) is dropped unread, updates carry no text,
//     and agent-written text that reaches an error is redacted first.
package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/driver"
)

// Name is the driver's name, as connect.json and the ledger spell it.
const Name = "acp"

// ProtocolVersion is the ACP version the connector speaks.
const ProtocolVersion = 1

// Defaults.
const (
	DefaultHandshakeTimeout = 2 * time.Minute
	DefaultCloseGrace       = 5 * time.Second
)

// modeConfirmWait is how long a session with no mode config option has to
// report the mode it was set to. A variable so tests need not wait it out.
var modeConfirmWait = 10 * time.Second

// Errors.
var (
	// ErrLoadUnsupported is a session/load asked of an agent that advertises
	// neither loadSession nor session resume.
	ErrLoadUnsupported = errors.New("acp: the agent advertises neither session/load nor session/resume")
	// ErrWrongAdapter is an agent that is not the pinned adapter.
	ErrWrongAdapter = errors.New("acp: the agent is not the pinned adapter")
)

// Options configures the driver.
type Options struct {
	// Adapter is the pinned adapter the driver runs.
	Adapter Adapter
	// Binary is the adapter executable, absolute: Locate's answer.
	Binary string
	// Args are the adapter's arguments; none for the pinned adapters.
	Args []string
	// Lookup reads the connector's environment for Adapter.Env;
	// os.LookupEnv when nil.
	Lookup func(string) (string, bool)
	// HandshakeTimeout bounds initialize, session/new or load, and setting the
	// mode.
	HandshakeTimeout time.Duration
	// CloseGrace is how long the adapter has to exit after its input closes,
	// and then after SIGTERM, before its process group is killed.
	CloseGrace time.Duration

	// trace is this package's tests' view of the wire.
	trace func(dir string, line []byte)
}

// Driver starts ACP sessions with one adapter.
type Driver struct {
	opts Options
	// loadSession is what the last initialize advertised: 0 unknown, 1 no,
	// 2 yes.
	loadSession atomic.Int32
}

var _ driver.Driver = (*Driver)(nil)

// New builds the driver.
func New(opts Options) (*Driver, error) {
	switch {
	case opts.Adapter.Name == "" || opts.Adapter.Package == "" || opts.Adapter.Version == "":
		return nil, errors.New("acp: the driver needs a pinned adapter")
	case !filepath.IsAbs(opts.Binary):
		return nil, fmt.Errorf("acp: the adapter executable %q is not an absolute path", opts.Binary)
	}
	if opts.Lookup == nil {
		opts.Lookup = os.LookupEnv
	}
	if opts.HandshakeTimeout <= 0 {
		opts.HandshakeTimeout = DefaultHandshakeTimeout
	}
	if opts.CloseGrace <= 0 {
		opts.CloseGrace = DefaultCloseGrace
	}
	return &Driver{opts: opts}, nil
}

// Name implements driver.Driver.
func (d *Driver) Name() string { return Name }

// Capabilities implements driver.Driver. LoadSession is what the installed
// adapter advertised at its last initialize, and the pinned version's until
// one has run; LoadSession itself checks again.
func (d *Driver) Capabilities() driver.Capabilities {
	load := d.opts.Adapter.LoadSession
	switch d.loadSession.Load() {
	case 1:
		load = false
	case 2:
		load = true
	}
	return driver.Capabilities{LoadSession: load, FollowUpPrompts: true, PermissionCallback: true}
}

// NewSession implements driver.Driver.
func (d *Driver) NewSession(ctx context.Context, cfg driver.SessionConfig) (driver.Session, error) {
	return d.open(ctx, cfg, "")
}

// LoadSession implements driver.Driver.
func (d *Driver) LoadSession(ctx context.Context, cfg driver.SessionConfig, sessionID string) (driver.Session, error) {
	if !validSessionID(sessionID) {
		return nil, fmt.Errorf("%w: %w: %q is not an ACP session id", driver.ErrNotStarted, driver.ErrUnusable, sessionID)
	}
	return d.open(ctx, cfg, sessionID)
}

// open starts the adapter and opens (loadID empty) or loads a session. Once
// the process exists, every failure ends its group and is not ErrNotStarted
// (driver invariant 4).
func (d *Driver) open(ctx context.Context, cfg driver.SessionConfig, loadID string) (driver.Session, error) {
	if cfg.Policy == nil || !filepath.IsAbs(cfg.Cwd) {
		return nil, fmt.Errorf("%w: %w: a session needs a policy and an absolute working directory", driver.ErrNotStarted, driver.ErrUnusable)
	}
	rules := cfg.Policy.Rules()
	mode, ok := d.opts.Adapter.Modes[rules.Mode]
	if !ok {
		return nil, fmt.Errorf("%w: %w: %w: %s has no asking mode for policy mode %q", driver.ErrNotStarted, driver.ErrUnusable, driver.ErrUnsafeMode, d.opts.Adapter.Name, rules.Mode)
	}
	if filepath.Clean(rules.WorkDir) != filepath.Clean(cfg.Cwd) {
		return nil, fmt.Errorf("%w: %w: the policy's working directory is not the session's", driver.ErrNotStarted, driver.ErrUnusable)
	}
	servers, err := wireServers(cfg.MCPServers)
	if err != nil {
		return nil, fmt.Errorf("%w: %w: %w", driver.ErrNotStarted, driver.ErrUnusable, err)
	}
	if d.opts.Adapter.Preflight != nil {
		if err := d.opts.Adapter.Preflight(cfg.Cwd, d.opts.Lookup); err != nil {
			// Configuration on this machine: the same session would fail the
			// same way, so it is not retried.
			return nil, fmt.Errorf("%w: %w: %w", driver.ErrNotStarted, driver.ErrUnusable, err)
		}
	}

	env := mergeEnv(cfg.Env, driver.BuildEnv(d.opts.Adapter.Env, d.opts.Lookup, nil))
	env = setEnv(env, d.opts.Adapter.SetEnv)
	worker, err := driver.StartWorker(ctx, cfg.Launcher, cfg.Scope, driver.Command{
		Path: d.opts.Binary, Args: append([]string{}, d.opts.Args...), Env: env, Dir: cfg.Cwd,
	})
	if err != nil {
		return nil, err
	}
	s := newSession(worker, cfg.Policy, mode, d.opts.CloseGrace, d.opts.trace)
	hctx, cancel := context.WithTimeout(ctx, d.opts.HandshakeTimeout)
	defer cancel()
	if err := s.handshake(hctx, d, cfg, servers, loadID); err != nil {
		s.abort()
		if ctxErr := hctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			err = fmt.Errorf("%w (%w)", err, ctxErr)
		}
		return nil, fmt.Errorf("%w%s", err, s.stderrNote())
	}
	return s, nil
}

// handshake is initialize, the session, and its mode.
func (s *session) handshake(ctx context.Context, d *Driver, cfg driver.SessionConfig, servers []wireServer, loadID string) error {
	caps, err := s.initialize(ctx, d.opts.Adapter)
	if err != nil {
		return err
	}
	if caps.LoadSession || caps.Resume {
		d.loadSession.Store(2)
	} else {
		d.loadSession.Store(1)
	}
	var opened sessionState
	if loadID == "" {
		opened, err = s.newSession(ctx, cfg.Cwd, servers, d.opts.Adapter.SessionMeta)
	} else {
		opened, err = s.loadSession(ctx, caps, loadID, cfg.Cwd, servers, d.opts.Adapter.SessionMeta)
	}
	if err != nil {
		return err
	}
	return s.enterAskingMode(ctx, opened)
}
