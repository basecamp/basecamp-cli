// Package driver is the connector's agent boundary: how a dispatched task
// becomes a working coding agent, and how the connector hears what it does.
//
// # The shape is ACP's
//
// The interface is Agent Client Protocol v1's session model, whatever speaks
// underneath. A Driver opens a session (session/new) or reloads one
// (session/load) in a working directory with an explicit set of MCP servers;
// a Session takes prompts, each returning a stop reason (session/prompt);
// progress arrives as a stream of updates (session/update); a turn is ended
// with Cancel (session/cancel); and a permission the agent asks for is
// answered by the connector's policy (session/request_permission). A spawn
// driver (claude -p, codex exec) is an adapter onto that shape: it maps its
// vendor stream onto the same updates and stop reasons, freezes the policy
// into flags it verifies, and cancels by ending the process group it started.
// So the ACP driver is one more driver, not a rewrite.
//
// # Invariants every driver holds
//
// Each is held by a test in the driver that implements it.
//
//  1. Nothing is inherited. A worker process gets exactly the environment in
//     SessionConfig.Env and each MCP server exactly MCPServer.Env; the
//     connector's own environment (which carries tokens of its host) never
//     reaches either. No secret is ever put in a process's argv.
//  2. The permission mode is set explicitly and verified. A session whose
//     agent did not confirm the mode the policy asked for is unsafe, and the
//     driver refuses to go on with it (ErrUnsafeMode) rather than run under
//     the host's own configuration.
//  3. A refusal is the driver's own record. A policy refusal is not
//     distinguishable from a cancel by the agent's stop reason, so every
//     refusal the driver made or observed is recorded once, through
//     SessionConfig.Refusals, at the moment it is made or observed; it is
//     reported as well as a Refusal on the prompt's result and as an update;
//     and a stop the connector did not ask for is never reported as
//     TurnCanceled. See "Refusals" below.
//  4. ErrNotStarted means no worker process ever existed. It is the only
//     start error after which the connector retries on its own, so a driver
//     returns it only when it can prove nothing ran; any doubt is some other
//     error. A configuration no retry can fix wraps ErrUnusable as well, and
//     is not retried. Whatever the error, a start that fails leaves no
//     process behind: either none was started, or the driver ended the one it
//     started — through Terminate, so the whole group goes — before
//     returning. A driver that cannot promise that returns a Session the
//     connector can Close instead of an error.
//  5. A worker is ended by the process group the driver started, never by
//     name. Close is idempotent and leaves no process of the session behind.
//  6. Content stays in the stream. Updates carry kinds, ids, tool names and
//     counts; they never carry the agent's text or a tool's input, so a sink
//     that logs an update cannot log content. What a sink does log from an
//     agent stream goes through the redaction rule (redact.go).
//
// # Refusals: where one is recorded, and when it counts as settled
//
// A refusal is a permission the agent asked for and did not get. It is
// recorded in the ledger, once, at the moment the driver answers the request
// — or, for an agent that answers its own requests under a mode the driver
// froze (claude -p), at the moment the driver first reads that it was
// refused. It is never held only in a session's memory, because a worker that
// exits before its result, a connector that crashes mid-turn, and a turn cut
// short by a deadline all end the session that memory lives in.
//
//  1. The driver calls SessionConfig.Refusals.RecordRefusal before it sends
//     its answer to the agent, or before it emits the update for a refusal
//     it observed. It calls it once per tool call id: a refusal the stream
//     announced and the result repeats is one refusal.
//  2. The dispatcher's recorder writes it to the attempt's row at once
//     (connector.Ledger.RecordRefusal: attempts.refusals, incremented while
//     the attempt is live). A write the ledger refuses is carried by the
//     recorder into the attempt's settlement instead, and logged.
//  3. The refusal is settled with its attempt: EndAttempt adds whatever the
//     recorder could not write, and the ended attempt's count is final. The
//     session's updates are drained before the attempt is released, and the
//     recorder is called before an update is emitted, so a worker that exits
//     between a refusal and its result has already recorded it.
//
// Once-ness is the driver's (a set of tool call ids per session), not a key in
// the ledger: it holds for as long as a session lives, which is as long as a
// refusal can be reported twice. A connector that restarts does not resume a
// session — its attempt is settled as lost and its task superseded — so a
// ledger key on (attempt, tool call) would buy nothing, and this is settled,
// not open.
//
// Where this can still be broken: a refusal the agent never reports — a tool
// it declined to ask for, or a denial its stream does not carry — is not a
// refusal the driver can record.
package driver

import (
	"context"
	"errors"
	"time"
)

// Driver starts and reloads sessions for one kind of coding agent.
type Driver interface {
	// Name is the driver's name as connect.json and the ledger spell it:
	// "claude", "codex", "acp".
	Name() string
	// Capabilities says what the driver supports beyond NewSession and Prompt.
	Capabilities() Capabilities
	// NewSession starts a worker and opens a session in cfg.Cwd.
	//
	// An error that wraps ErrNotStarted means no process ever existed, and
	// the connector may retry the start once. Any other error from a start
	// that launched a process wraps a *StartError carrying that process, whose
	// group the driver has already asked to end: the connector confirms it
	// gone (ConfirmGroupGone) before it settles anything, however long the
	// driver's own handshake took to fail.
	NewSession(ctx context.Context, cfg SessionConfig) (Session, error)
	// LoadSession reopens a session by the id an earlier Session reported,
	// where Capabilities().LoadSession is true. Its errors read as
	// NewSession's, and leave no process behind either.
	LoadSession(ctx context.Context, cfg SessionConfig, sessionID string) (Session, error)
}

// Capabilities are what a driver advertises, as an ACP agent advertises its
// own at initialize.
type Capabilities struct {
	// LoadSession: LoadSession works, so a follow-up after the worker ended
	// can continue its conversation.
	LoadSession bool
	// FollowUpPrompts: a live session takes further prompts, so a follow-up
	// is delivered into the same session rather than as a new attempt.
	FollowUpPrompts bool
	// PermissionCallback: the agent asks, and PermissionPolicy.Decide answers
	// each request. False for a spawn driver, whose permissions are frozen
	// into flags from PermissionPolicy.Rules before the process starts.
	PermissionCallback bool
}

// Session is one live conversation with a worker.
type Session interface {
	// ID is the agent's session id (ACP sessionId, Claude Code's session_id).
	// It is known when NewSession returns.
	ID() string
	// Process is the worker's process, or the zero Process when the session
	// runs somewhere the connector cannot signal.
	Process() Process
	// Prompt sends one prompt and blocks until the turn ends. The first
	// prompt of a session is its handshake: a driver that verifies the
	// agent's mode on it returns ErrUnsafeMode and ends the session. A ctx
	// that ends makes Prompt return ctx's error without ending the turn; use
	// Cancel for that.
	Prompt(ctx context.Context, prompt string) (PromptResult, error)
	// Updates streams the session's progress. It is closed when the session
	// ends. A consumer that stops reading does not stall the agent: a driver
	// drops updates rather than block.
	Updates() <-chan Update
	// Cancel ends the turn in flight. Prompt then returns TurnCanceled.
	// With no turn in flight it does nothing.
	Cancel(ctx context.Context) error
	// Close ends the session and its worker: the process group is signaled,
	// given grace, and killed. Idempotent; safe concurrently with Prompt,
	// which then returns an error.
	Close() error
	// Done is closed once the worker has exited, however it exited.
	Done() <-chan struct{}
	// Exit is how the worker exited; meaningful once Done is closed.
	Exit() Exit
}

// SessionConfig is everything a driver needs to start a session. The
// dispatcher builds it from the task's record; the driver adds nothing of its
// own beyond its binary and its flags.
type SessionConfig struct {
	// Cwd is the approved working directory, absolute.
	Cwd string
	// Env is the worker process's whole environment, as KEY=VALUE. Nothing
	// else is inherited (invariant 1). BuildEnv makes one from an allowlist.
	Env []string
	// MCPServers are the only MCP servers the agent gets. A driver makes the
	// agent ignore every other MCP configuration it would otherwise load.
	MCPServers []MCPServer
	// Policy answers permissions.
	Policy PermissionPolicy
	// Launcher wraps the worker command. Nil means DirectLauncher.
	Launcher Launcher
	// Scope is what the launcher is told the worker is for.
	Scope Scope
	// PrivateDir is an owner-only directory the driver may write session
	// files into (an MCP config, say). The driver removes what it wrote when
	// the session is closed; the dispatcher sweeps the directory on start.
	PrivateDir string
	// Refusals records every refusal at the moment it is made or observed.
	// Nil records nothing; the dispatcher always sets it.
	Refusals RefusalRecorder
	// Redaction is what the driver takes out of every error it returns and
	// every text an update or a stderr tail carries (redact.go). The driver
	// adds the environment it builds, its MCP servers' environments and
	// PrivateDir to it.
	Redaction Redaction
}

// MCPServer is one stdio MCP server handed to the agent, as ACP's
// mcpServers[] entry.
type MCPServer struct {
	// Name is the server's name as the agent's tools will be prefixed.
	Name string
	// Command is the executable, absolute.
	Command string
	// Args are its arguments. Never a secret: argv is readable by every
	// process on the machine.
	Args []string
	// Env is the server's whole environment, KEY -> VALUE. Declared
	// explicitly, never counted on to be inherited: some agents pass their
	// own environment down and some pass almost nothing.
	Env map[string]string
}

// Process is a worker process the connector started.
type Process struct {
	// PID is the process's id; zero when there is none to signal.
	PID int
	// PGID is its process group, which Close signals. A driver starts every
	// worker as the leader of a new group, so PGID == PID.
	PGID int
	// StartedAt is when the driver started it, to tell the process from a
	// later one that reused its id.
	StartedAt time.Time
}

// Exit is how a worker ended.
type Exit struct {
	// Code is the exit status, or -1 when a signal ended the process.
	Code int
	// Signaled is true when a signal ended it.
	Signaled bool
	// Err is a failure to wait on the process at all.
	Err error
}

// TurnStop is why a prompt turn ended: ACP v1's stop reasons.
type TurnStop string

const (
	// TurnEndTurn is the agent finishing its turn.
	TurnEndTurn TurnStop = "end_turn"
	// TurnMaxTokens is the token limit.
	TurnMaxTokens TurnStop = "max_tokens"
	// TurnMaxTurnRequests is the agent's own request budget for the turn.
	TurnMaxTurnRequests TurnStop = "max_turn_requests"
	// TurnRefusal is the agent refusing to continue.
	TurnRefusal TurnStop = "refusal"
	// TurnCanceled is a cancel the connector asked for, and only that
	// (invariant 3). The value is ACP's spelling.
	TurnCanceled TurnStop = "cancelled" //nolint:misspell // ACP's wire value
)

// PromptResult is a finished turn.
type PromptResult struct {
	Stop TurnStop
	// Refusals are the permissions refused during the turn (invariant 3).
	Refusals []Refusal
	// Usage is the turn's token use, where the agent reports it.
	Usage Usage
}

// Refusal is one permission the policy refused.
type Refusal struct {
	// ToolCallID is the agent's id for the call.
	ToolCallID string
	// Tool is the tool's name or ACP kind; never its input.
	Tool string
}

// RefusalRecorder records a refusal at the moment a driver makes or observes
// it (see "Refusals" above). RecordRefusal must not block for long: a driver
// calls it on the goroutine that reads the agent's stream.
type RefusalRecorder interface {
	RecordRefusal(ctx context.Context, r Refusal) error
}

// Usage is token accounting.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
	// ContextUsed and ContextSize are ACP usage_update's {used, size}, where
	// known.
	ContextUsed int64
	ContextSize int64
}

// UpdateKind names a session update, as ACP's sessionUpdate does.
type UpdateKind string

const (
	UpdateToolCall          UpdateKind = "tool_call"
	UpdateToolCallUpdate    UpdateKind = "tool_call_update"
	UpdateUsage             UpdateKind = "usage_update"
	UpdateAgentMessageChunk UpdateKind = "agent_message_chunk"
	// UpdatePlan is optional: no adapter the spike ran emitted one.
	UpdatePlan UpdateKind = "plan"
	// UpdatePermission is a permission decision the driver made or observed.
	UpdatePermission UpdateKind = "permission"
)

// ToolStatus is a tool call's status.
type ToolStatus string

const (
	ToolPending    ToolStatus = "pending"
	ToolInProgress ToolStatus = "in_progress"
	ToolCompleted  ToolStatus = "completed"
	ToolFailed     ToolStatus = "failed"
)

// ToolKind is ACP's tool kind.
type ToolKind string

const (
	ToolRead    ToolKind = "read"
	ToolEdit    ToolKind = "edit"
	ToolDelete  ToolKind = "delete"
	ToolMove    ToolKind = "move"
	ToolSearch  ToolKind = "search"
	ToolExecute ToolKind = "execute"
	ToolThink   ToolKind = "think"
	ToolFetch   ToolKind = "fetch"
	ToolOther   ToolKind = "other"
)

// Update is one piece of progress. It carries no content (invariant 6):
// progress is for liveness, budgets and the ledger, never for reading what
// the agent said.
type Update struct {
	Kind UpdateKind
	At   time.Time

	// ToolCallID, Tool, ToolKind and Status describe a tool call.
	ToolCallID string
	// Tool is the tool's name ("Bash", "mcp__basecamp__basecamp_connect").
	Tool     string
	ToolKind ToolKind
	Status   ToolStatus

	// Usage is set on UpdateUsage.
	Usage *Usage
	// Chars is the length of an agent message chunk, whose text is not
	// carried.
	Chars int
	// Allowed is set on UpdatePermission: whether the policy allowed it.
	Allowed bool
}

// PermissionPolicy is the connector's answer to what a worker may do.
// Permission answers are policy, not containment: the worker still runs with
// the operator's ambient authority, and nothing here is a sandbox.
type PermissionPolicy interface {
	// Decide answers one request, for drivers that ask
	// (Capabilities.PermissionCallback).
	Decide(ctx context.Context, req PermissionRequest) PermissionDecision
	// Rules is the same policy, pre-decided, for drivers whose permissions
	// are fixed before the worker starts.
	Rules() PermissionRules
}

// PermissionRequest is ACP's session/request_permission, reduced to what a
// policy decides on.
type PermissionRequest struct {
	ToolCallID string
	Tool       string
	Kind       ToolKind
	// Locations are the paths the call touches, where the agent says.
	Locations []string
	// Options are the choices the agent offers. A driver selects by kind,
	// never by id or label: ids are not portable across agents.
	Options []PermissionOption
}

// PermissionOption is one choice the agent offers.
type PermissionOption struct {
	ID   string
	Kind PermissionOptionKind
}

// PermissionOptionKind is ACP's option kind.
type PermissionOptionKind string

const (
	AllowOnce    PermissionOptionKind = "allow_once"
	AllowAlways  PermissionOptionKind = "allow_always"
	RejectOnce   PermissionOptionKind = "reject_once"
	RejectAlways PermissionOptionKind = "reject_always"
)

// PermissionDecision is the policy's answer. A driver answers with the offered
// option of kind AllowOnce or RejectOnce, and refuses when the kind it needs
// is not offered.
type PermissionDecision struct {
	Allow bool
}

// PermissionRules is a policy pre-decided.
type PermissionRules struct {
	// Mode is the asking mode the agent must run in and confirm.
	Mode PermissionMode
	// WorkDir is where edits are allowed; everything outside it is refused.
	WorkDir string
	// AllowKinds are the tool kinds allowed without asking, besides edits
	// inside WorkDir.
	AllowKinds []ToolKind
	// AllowMCPServers are the MCP servers whose every tool is allowed.
	AllowMCPServers []string
}

// PermissionMode is the connector's name for an agent's permission mode. A
// driver maps it to the agent's own mode id and verifies the agent reports
// that id back.
type PermissionMode string

const (
	// ModeEditsInWorkDir allows edits inside the working directory, and
	// refuses, without asking anyone, whatever the rules do not allow.
	ModeEditsInWorkDir PermissionMode = "edits_in_workdir"
)

// Launcher wraps the worker command: the seam where a sandbox launcher
// (sandbox-run) takes the dispatch. Scopes in, working directory and receipts
// out.
type Launcher interface {
	// Launch returns the command that actually runs and the directory it runs
	// in. A launcher refuses a request whose scope it cannot honor.
	Launch(ctx context.Context, req LaunchRequest) (Launched, error)
	// Receipts are what the launcher confirms the worker did, for the attempt
	// the scope named. The direct launcher confirms nothing.
	Receipts(ctx context.Context, attemptID string) ([]Receipt, error)
}

// Scope is what a worker is for, as the launcher is told.
type Scope struct {
	TaskID    int64
	AttemptID string
	// EventIDs are the events the task may cover. Only the originating event
	// has been handed to the worker when the session starts; the others are
	// exposed as they are prompted.
	EventIDs []int64
	// WorkDir is the approved working directory the record carries.
	WorkDir string
	Class   string
}

// Command is a process to run: path, argv (without the path) and the whole
// environment.
type Command struct {
	Path string
	Args []string
	Env  []string
	Dir  string
}

// LaunchRequest is a worker command and its scope.
type LaunchRequest struct {
	Scope   Scope
	Command Command
}

// Launched is what runs.
type Launched struct {
	Command Command
	// WorkDir is the directory the worker works in: Scope.WorkDir for the
	// direct launcher, a broker-owned scope under a sandbox.
	WorkDir string
}

// Receipt is something a launcher confirms a worker posted.
type Receipt struct {
	Kind string
	ID   int64
	URL  string
}

// DirectLauncher runs the worker as it is, in the scope's directory.
type DirectLauncher struct{}

// Launch implements Launcher.
func (DirectLauncher) Launch(_ context.Context, req LaunchRequest) (Launched, error) {
	if req.Scope.WorkDir == "" {
		return Launched{}, errors.New("driver: a launch needs the working directory the record carries")
	}
	cmd := req.Command
	cmd.Dir = req.Scope.WorkDir
	return Launched{Command: cmd, WorkDir: req.Scope.WorkDir}, nil
}

// Receipts implements Launcher.
func (DirectLauncher) Receipts(context.Context, string) ([]Receipt, error) { return nil, nil }

// StartError is a start that failed after it launched a process. The
// driver has asked the process's group to end; the connector owns confirming
// it gone before it settles the attempt or releases its directory.
type StartError struct {
	Process Process
	Err     error
}

func (e *StartError) Error() string {
	return "driver: the worker started and then failed: " + e.Err.Error()
}
func (e *StartError) Unwrap() error { return e.Err }

// StartedProcess is the process a failed start launched, if it launched one.
func StartedProcess(err error) Process {
	var started *StartError
	if errors.As(err, &started) {
		return started.Process
	}
	return Process{}
}

// DefaultGrace is how long a worker's process group has between SIGTERM and
// SIGKILL.
const DefaultGrace = 10 * time.Second

// Errors a driver reports.
var (
	// ErrNotStarted wraps a start that failed before any worker process
	// existed (invariant 4): the binary is missing, the launcher refused, the
	// fork failed. Only this is retried automatically.
	ErrNotStarted = errors.New("driver: the worker was not started")
	// ErrUnusable wraps ErrNotStarted for a configuration no retry can fix:
	// a mode the driver cannot express, a policy for another directory, an
	// MCP server without a command. No process existed, and starting again
	// would fail the same way, so the connector does not retry it.
	ErrUnusable = errors.New("driver: the session's configuration cannot start a worker")
	// ErrUnsafeMode is an agent that did not confirm the permission mode the
	// policy asked for (invariant 2). The session is ended.
	ErrUnsafeMode = errors.New("driver: the agent did not confirm the permission mode asked for")
	// ErrSessionUnverified is a session that started but is not the one the
	// connector asked for: an MCP server the agent did not connect, or a
	// session id that is not the one requested. The driver ends such a
	// session rather than let a worker run without the tools its dispatch
	// needs — a worker with no Basecamp tools can neither read its dispatch
	// nor report it, and would otherwise finish with the mention unanswered
	// (card 23's finding). A driver's own sentinel for one of these wraps
	// this one.
	ErrSessionUnverified = errors.New("driver: the session is not the one the connector asked for")
	// ErrSessionEnded is a call on a session whose worker is gone.
	ErrSessionEnded = errors.New("driver: the session has ended")
)
