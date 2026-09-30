package driver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

// A preflight starts a worker's own program the way a session would — the
// same binary, found on the same PATH, with the same environment — and asks
// it three things without any work and without a model call: does it start,
// does it know every flag a session passes it, and is it logged in. What it
// finds is said in a person's words, with the fix, because the person reading
// it is the one who runs the agent, not the one who mentioned it.

// Preflighter is a driver that can check its worker before any work is given
// to it.
type Preflighter interface {
	Preflight(ctx context.Context, policy PermissionPolicy) Preflight
}

// Preflight is what a worker's preflight found.
type Preflight struct {
	// Product is the worker's name for a person: "Claude Code".
	Product string
	// Version is what the worker said its version is; empty when it did not
	// start.
	Version string
	Checks  []PreflightCheck
}

// Failed is the first check that failed.
func (p Preflight) Failed() (PreflightCheck, bool) {
	for _, c := range p.Checks {
		if c.Status == PreflightFail {
			return c, true
		}
	}
	return PreflightCheck{}, false
}

// PreflightCheck is one question a preflight asked.
type PreflightCheck struct {
	Name    string
	Status  PreflightStatus
	Message string
	// Hint is the fix, where one is known.
	Hint string
}

// PreflightStatus is how a check went. A warning is something the preflight
// could not settle; it never stops anything.
type PreflightStatus string

const (
	PreflightPass PreflightStatus = "pass"
	PreflightFail PreflightStatus = "fail"
	PreflightWarn PreflightStatus = "warn"
)

// The checks, by name.
const (
	PreflightStarts = "starts"
	PreflightFlags  = "flags"
	PreflightLogin  = "login"
)

// PreflightTimeout bounds each of a preflight's runs.
const PreflightTimeout = 30 * time.Second

// WorkerProbe is how one driver's preflight runs.
type WorkerProbe struct {
	// Product is the worker's name for a person: "Claude Code".
	Product string
	// Binary is the program a session starts, found on PATH as a session
	// finds it.
	Binary string
	// Env is the environment a session gets.
	Env []string
	// Help is the arguments that list the flags Flags must be among.
	Help []string
	// Flags are the flags a session passes.
	Flags []string
	// Update is how a person updates the worker.
	Update string
	// Login is the arguments that ask whether the worker is logged in, with
	// no model call.
	Login []string
	// LoggedIn reads Login's answer: whether the worker said it is logged in,
	// and whether it said anything that could be read.
	LoggedIn func(ProbeResult) (loggedIn, known bool)
	// LoginFix is what a person does when the worker is logged out.
	LoginFix string
	// LoginWarns is a login check that never fails: its answer is a guess.
	LoginWarns bool
	// Timeout bounds each run; PreflightTimeout when zero.
	Timeout time.Duration
	// Run runs one probe; RunProbe when nil. A test seam.
	Run func(ctx context.Context, cmd Command, timeout time.Duration) ProbeResult
}

// ProbeResult is one short run of a worker's program.
type ProbeResult struct {
	Stdout, Stderr string
	// Exit is the exit status; -1 when the program did not exit on its own.
	Exit int
	// NotFound is a program PATH does not have.
	NotFound bool
	// TimedOut is a program that did not answer in time.
	TimedOut bool
	// Err is why the program could not be run, when it could not.
	Err error
}

// probeOutputLimit bounds what a probe keeps of each stream.
const probeOutputLimit = 256 << 10

// RunProbe runs cmd with no input, bounded by timeout. The program is found as
// StartWorker finds it.
func RunProbe(ctx context.Context, cmd Command, timeout time.Duration) ProbeResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ec := exec.CommandContext(ctx, cmd.Path, cmd.Args...) //nolint:gosec // G204: the driver's own binary and flags, never content
	ec.Dir = cmd.Dir
	ec.Env = cmd.Env
	if ec.Env == nil {
		ec.Env = []string{}
	}
	ec.WaitDelay = 2 * time.Second
	probeInItsOwnGroup(ec)
	var stdout, stderr limitedBuffer
	stdout.max, stderr.max = probeOutputLimit, probeOutputLimit
	ec.Stdout, ec.Stderr = &stdout, &stderr
	err := ec.Run()
	out := ProbeResult{Stdout: stdout.String(), Stderr: stderr.String(), Exit: -1}
	if ec.ProcessState != nil {
		out.Exit = ec.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		out.TimedOut = true
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist) && ec.ProcessState == nil && !isFile(ec.Path):
		// Missing only when the program itself is: a program that is there
		// but whose interpreter or loader is gone fails its exec with the
		// same ENOENT, and is a program that can't start, not one PATH lacks.
		out.NotFound = true
		out.Err = err
	case errors.As(err, &exitErr):
	case err != nil:
		out.Err = err
	}
	return out
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// Check runs the preflight: the start first, and the rest only when it
// started.
func (w WorkerProbe) Check(ctx context.Context) Preflight {
	out := Preflight{Product: w.Product}
	red := NewRedactor(Redaction{Env: w.Env})
	path, _ := exec.LookPath(w.Binary)

	started := w.run(ctx, "--version")
	if c, ok := w.startFailure(started, path, red); ok {
		out.Checks = append(out.Checks, c)
		return out
	}
	out.Version = versionIn(started.Stdout)
	named := w.Product
	if out.Version != "" {
		named += " " + out.Version
	}
	out.Checks = append(out.Checks, PreflightCheck{Name: PreflightStarts, Status: PreflightPass, Message: named + " starts (" + path + ")"})

	out.Checks = append(out.Checks, w.flagsCheck(ctx, named, red))
	if len(w.Login) > 0 {
		out.Checks = append(out.Checks, w.loginCheck(ctx, red))
	}
	return out
}

func (w WorkerProbe) run(ctx context.Context, args ...string) ProbeResult {
	run := w.Run
	if run == nil {
		run = RunProbe
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = PreflightTimeout
	}
	return run(ctx, Command{Path: w.Binary, Args: args, Env: w.Env}, timeout)
}

func (w WorkerProbe) command(args ...string) string {
	return "`" + strings.Join(append([]string{w.Binary}, args...), " ") + "`"
}

// launcherMissing is what a shell or a wrapper says when the program it
// hands over to is not there.
var launcherMissing = regexp.MustCompile(`(?i)no such file or directory|not found|cannot execute`)

// startFailure is the start check when the worker did not start.
func (w WorkerProbe) startFailure(r ProbeResult, path string, red *Redactor) (PreflightCheck, bool) {
	c := PreflightCheck{Name: PreflightStarts, Status: PreflightFail}
	said := red.Stderr(r.Stderr)
	version := w.command("--version")
	switch {
	case r.NotFound:
		c.Message = fmt.Sprintf("%s isn't installed here: %s is not on the PATH the connector starts with", w.Product, w.Binary)
		c.Hint = fmt.Sprintf("Install %s, or put %s on the PATH the connector starts with.", w.Product, w.Binary)
	case r.TimedOut:
		c.Message = fmt.Sprintf("%s didn't answer %s in time", w.Product, version)
		c.Hint = fmt.Sprintf("Run %s yourself to see what it's waiting for.", version)
	case r.Err != nil:
		c.Message = fmt.Sprintf("%s couldn't be started: %s", w.Product, red.Sanitize(r.Err.Error()))
		c.Hint = fmt.Sprintf("Run %s yourself to see why.", version)
	case r.Exit == 0:
		return PreflightCheck{}, false
	case r.Exit == 126 || r.Exit == 127 || launcherMissing.MatchString(said):
		c.Message = fmt.Sprintf("The %s on your PATH is a launcher that couldn't find %s (%s)", w.Binary, w.Product, orExit(said, r.Exit))
		c.Hint = fmt.Sprintf("Reinstall %s, or fix the launcher at %s.", w.Product, path)
	default:
		c.Message = fmt.Sprintf("%s couldn't start: %s exited with status %d (%s)", w.Product, version, r.Exit, orExit(said, r.Exit))
		c.Hint = fmt.Sprintf("Run %s yourself to see why.", version)
	}
	return c, true
}

func orExit(said string, exit int) string {
	if said == "" {
		return fmt.Sprintf("it exited with status %d and said nothing", exit)
	}
	return said
}

// flagsCheck asks the worker's help for every flag a session passes. A
// worker too old to know one would refuse the session it was started for.
func (w WorkerProbe) flagsCheck(ctx context.Context, named string, red *Redactor) PreflightCheck {
	c := PreflightCheck{Name: PreflightFlags}
	r := w.run(ctx, w.Help...)
	if r.Exit != 0 || r.TimedOut || r.Err != nil {
		c.Status = PreflightFail
		c.Message = fmt.Sprintf("%s wouldn't list its options: %s failed (%s)", named, w.command(w.Help...), orExit(red.Stderr(r.Stderr), r.Exit))
		c.Hint = fmt.Sprintf("Run %s yourself to see why.", w.command(w.Help...))
		return c
	}
	missing := MissingFlags(r.Stdout+"\n"+r.Stderr, w.Flags)
	if len(missing) > 0 {
		c.Status = PreflightFail
		c.Message = fmt.Sprintf("%s is too old for the connector — update it", named)
		c.Hint = fmt.Sprintf("It doesn't know %s. %s", strings.Join(missing, ", "), w.Update)
		return c
	}
	c.Status = PreflightPass
	c.Message = fmt.Sprintf("knows all %d options the connector passes", len(w.Flags))
	return c
}

func (w WorkerProbe) loginCheck(ctx context.Context, red *Redactor) PreflightCheck {
	c := PreflightCheck{Name: PreflightLogin}
	r := w.run(ctx, w.Login...)
	loggedIn, known := false, false
	if !r.TimedOut && r.Err == nil {
		loggedIn, known = w.LoggedIn(r)
	}
	switch {
	case known && loggedIn:
		c.Status = PreflightPass
		c.Message = "logged in"
	case known:
		c.Status = PreflightFail
		c.Message = fmt.Sprintf("%s is logged out on this computer — %s", w.Product, w.LoginFix)
		if w.LoginWarns {
			c.Status = PreflightWarn
			c.Message = fmt.Sprintf("%s may be logged out on this computer — %s", w.Product, w.LoginFix)
		}
	default:
		c.Status = PreflightWarn
		c.Message = fmt.Sprintf("couldn't tell whether %s is logged in: %s answered %s", w.Product, w.command(w.Login...), orExit(red.Stderr(r.Stderr), r.Exit))
	}
	return c
}

var versionPattern = regexp.MustCompile(`\d+\.\d+[0-9A-Za-z.+-]*`)

// versionIn is the version a --version answer names.
func versionIn(stdout string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(stdout), "\n")
	return versionPattern.FindString(first)
}

// FlagsOf is every flag in a command line, once each, in order. A lone "-"
// (read the prompt from stdin) is an argument, not a flag.
func FlagsOf(args []string) []string {
	var flags []string
	for _, a := range args {
		if len(a) > 1 && a != "--" && strings.HasPrefix(a, "-") && !slices.Contains(flags, a) {
			flags = append(flags, a)
		}
	}
	return flags
}

// MissingFlags is the flags help does not list. A flag counts as listed only
// as a whole word: --tools is not found inside --allowed-tools.
func MissingFlags(help string, flags []string) []string {
	var missing []string
	for _, f := range flags {
		listed := regexp.MustCompile(`(^|[\s,\[(|])` + regexp.QuoteMeta(f) + `($|[\s,=\])|<])`)
		if !listed.MatchString(help) {
			missing = append(missing, f)
		}
	}
	return missing
}
