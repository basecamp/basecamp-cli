package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/connector"
	"github.com/basecamp/basecamp-cli/internal/connector/console"
	"github.com/basecamp/basecamp-cli/internal/connector/runner"
	"github.com/basecamp/basecamp-cli/internal/connector/setup"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/tui"
)

// `basecamp connect console`: the connector's console, in the terminal. It
// is a process of its own that reads what `status` reads, the same way: the
// ledger read-only, the lock's metadata without the lock, the runner's
// state file and settings as they are on disk. It writes nothing, so it
// runs beside a connector, or without one, and a fault in it cannot reach
// the connector. What the person types to the orchestrator goes the way
// `basecamp connect say` goes: over the runner's socket.

func newConnectConsoleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "console",
		Short: "Watch a profile's connector, and talk to its orchestrator",
		Long: `Open a full-screen view of a profile's connector in this terminal: whether
it is running, what is being worked on now and by which coding agent, what
was done, which requests it heard, and whether it is well: the checks
basecamp connect doctor makes, and which coding agents are installed and at
what version. Its settings are shown as they are on disk.

It runs beside the connector, and whether or not one is running: open it
first and a connector started afterwards shows as it comes up. Closing it
leaves the connector as it was.

When the connector does the work itself, press / to tell its orchestrator
something, as basecamp connect say does. Your word there is the owner's
word.

It needs a terminal. For the same facts as text or JSON, use basecamp
connect status and basecamp connect doctor.`,
		Example: `  basecamp connect console -P agent`,
		Args:    cobra.NoArgs,
		RunE:    runConnectConsole,
	}
}

func runConnectConsole(cmd *cobra.Command, _ []string) error {
	if os.Getenv("BASECAMP_TOKEN") != "" {
		return errEnvTokenShadows("the console checks the agent its profile holds, and BASECAMP_TOKEN would override it")
	}
	p, err := loadConnectProfile(cmd)
	if err != nil {
		return err
	}
	stateDir, err := connectStatePath(p.name, p.file, false)
	if err != nil {
		return err
	}
	view := &connectConsole{p: p, stateDir: stateDir}
	go view.learnNames(cmd.Context())

	err = console.Run(cmd.Context(), console.Options{
		Snapshot: view.snapshot,
		Health:   view.health,
		Say:      view.say,
		Open:     hostutil.OpenBrowser,
		Theme:    tui.ResolveTheme(tui.DetectDark()),
	})
	if errors.Is(err, tui.ErrNotInteractive) {
		return output.ErrUsageHint("The console needs a terminal to draw in",
			"For the same facts as text or JSON: basecamp connect status -P "+richtext.ShellQuote(p.name)+" and basecamp connect doctor -P "+richtext.ShellQuote(p.name))
	}
	return err
}

// say gives the running connector's orchestrator a message from the person
// and waits for its answer, which is as long as the orchestrator's turn.
func (c *connectConsole) say(ctx context.Context, message string) (string, error) {
	_, stateDir, _ := c.current()
	resp, err := runner.Call(ctx, runner.SocketPath(connectRunnerDir(stateDir)), runner.Request{Op: runner.OpSay, Message: message}, 0)
	if errors.Is(err, runner.ErrNotRunning) {
		return "", errors.New("no connector is running with an orchestrator for this profile")
	}
	if err != nil {
		return "", err
	}
	return resp.Reply, nil
}

// health runs doctor's checks and asks each coding agent's CLI its version.
func (c *connectConsole) health(ctx context.Context) *console.Health {
	health := &console.Health{}
	var wg sync.WaitGroup
	wg.Go(func() { health.Checks = c.checks(ctx) })
	wg.Go(func() { health.Agents = runner.InstalledAgents(ctx, nil) })
	wg.Wait()
	health.At = time.Now()
	return health
}

// checks are doctor's checks as doctor run now would make them: against
// connect.json as it stands, not as it stood when the console opened.
func (c *connectConsole) checks(ctx context.Context) []console.Check {
	p, _, err := c.current()
	if err != nil {
		return []console.Check{{Name: "connect.json", Status: setup.StatusFail,
			Message: "It cannot be used as it now stands: " + setup.ErrorText(err),
			Hint:    "Run basecamp connect setup -P " + richtext.ShellQuote(p.name) + " again, then press r."}}
	}
	var checks []console.Check
	for _, check := range connectDoctorChecks(ctx, p) {
		checks = append(checks, console.Check{Name: check.Name, Status: check.Status, Message: check.Message, Hint: check.Hint})
	}
	return checks
}

// connectConsole reads what the console shows.
type connectConsole struct {
	mu sync.Mutex
	// p is the profile, its file as last read, and stateDir where its
	// connector keeps its state, as last found (current).
	p        connectProfile
	stateDir string
	names    connectConsoleNames
}

// connectConsoleNames are what Basecamp calls the projects and people
// connect.json names by id, once read, and err why they could not be. The
// maps are replaced whole and never changed, so a copy is read without the
// lock.
type connectConsoleNames struct {
	projects map[int64]string
	people   map[int64]string
	err      string
}

// connectConsoleRecent is how many requests and how many finished workers
// the console lists.
const connectConsoleRecent = 30

// snapshot reads the connector's state as it stands. A part that cannot be
// read is left empty and named in Notes: the console shows what it can.
func (c *connectConsole) snapshot(ctx context.Context) *console.Snapshot {
	// The lock is not held while the ledger and the runner are read: what
	// the person tells the orchestrator does not wait behind a snapshot.
	p, stateDir, loadErr := c.current()
	c.mu.Lock()
	names := c.names
	c.mu.Unlock()
	file := p.file
	snap := &console.Snapshot{At: time.Now().UTC(), Profile: p.name, Now: []runner.WorkerActivity{}, Done: []runner.WorkerActivity{},
		Requests: []console.Request{}, Held: []connector.HeldStatus{}}
	if loadErr != nil {
		snap.Notes = append(snap.Notes, "connect.json cannot be read as it now stands, so this shows it as it was last read: "+setup.ErrorText(loadErr))
	}
	if names.err != "" {
		snap.Notes = append(snap.Notes, "Names could not be read from Basecamp, so projects and people are shown by id: "+names.err)
	}
	snap.AccountID = file.AccountID
	snap.Agent = names.person(file.Agent.PersonID)

	for id, project := range file.Projects {
		snap.Projects = append(snap.Projects, console.Project{
			ID: id, Name: names.projects[id], URL: connectConsoleProjectURL(file.AccountID, id),
			WatchCompletions: project.WatchCompletions, Campfires: len(project.ListenCampfires),
		})
	}
	slices.SortFunc(snap.Projects, func(a, b console.Project) int {
		// Named projects by name, then the rest by id.
		switch {
		case a.Name != "" && b.Name != "":
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case a.Name != "":
			return -1
		case b.Name != "":
			return 1
		}
		return int(a.ID - b.ID)
	})

	snap.Trust = console.Trust{
		Mode: string(file.Trust.Mode), Operator: names.person(file.Trust.OperatorID),
		Allowlist: names.persons(file.Trust.AllowlistIDs), AllowAssignments: file.Trust.AllowAssignments,
		Agents: names.persons(file.Trust.AgentIDs),
	}

	pings := readConnectPingsView(ctx, nil, p.name, file)
	if hears, known := pings.Hears(); known {
		snap.Pings.Hears = &hears
	}
	switch {
	case pings.Running.Running && pings.Running.Pings != nil:
		snap.Pings.Because = pings.Running.Pings.Because
	case pings.Saved != nil:
		snap.Pings.Because = pings.Saved.Because
		if snap.Pings.Hears == nil {
			snap.Pings.Hears = &pings.Saved.Hears
		}
	}
	snap.Pings.Twice = pings.Twice()
	if pings.SavedErr != nil {
		snap.Pings.Error = errorMessage(pings.SavedErr)
	}
	for id, trust := range file.Pings {
		snap.Pings.Guests = append(snap.Pings.Guests, console.PingGuests{PingID: id, People: names.persons(trust.Participants)})
	}
	slices.SortFunc(snap.Pings.Guests, func(a, b console.PingGuests) int { return int(a.PingID - b.PingID) })

	snap.Connector.Running = pings.Running.Running
	if holder, ok := connector.InstanceHolder(stateDir, file.AccountID, file.Agent.PersonID); ok && snap.Connector.Running {
		snap.Connector.PID, snap.Connector.StartedAt = holder.PID, holder.StartedAt
	}

	readConnectConsoleLedger(ctx, snap, stateDir, names)
	readConnectConsoleRunner(ctx, snap, stateDir, connectRunnerConfigPath(p.path), names)
	return snap
}

// current is the profile as connect.json has it now and where its connector
// keeps its state now, both looked up again: setup changes the file, and a
// connector's first start under a profile moves the state directory an
// older CLI left there. When connect.json cannot be read as it stands, it
// is the last reading that could be, and why this one could not.
func (c *connectConsole) current() (p connectProfile, stateDir string, problem error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fresh, err := setup.Load(c.p.path); err != nil {
		problem = err
	} else {
		c.p.file = fresh
	}
	if dir, err := connectStatePath(c.p.name, c.p.file, false); err == nil {
		c.stateDir = dir
	}
	return c.p, c.stateDir, problem
}

// readConnectConsoleLedger adds what the ledger says: the hold, the last run, the held
// records and the latest requests.
func readConnectConsoleLedger(ctx context.Context, snap *console.Snapshot, stateDir string, names connectConsoleNames) {
	ledger, err := connector.OpenLedgerReadOnly(ctx, filepath.Join(stateDir, connector.LedgerFile))
	if errors.Is(err, os.ErrNotExist) {
		snap.Notes = append(snap.Notes, "This profile's connector has not run yet, so it has heard nothing.")
		return
	}
	if err != nil {
		snap.Notes = append(snap.Notes, "The connector's ledger could not be read: "+setup.ErrorText(err))
		return
	}
	defer func() { _ = ledger.Close() }()
	status, err := ledger.Status(ctx)
	if err != nil {
		snap.Notes = append(snap.Notes, "The connector's ledger could not be read: "+setup.ErrorText(err))
		return
	}
	snap.Connector.Hold, snap.Connector.LastChange, snap.Held = status.Hold, status.Connection, status.Held
	recent, err := ledger.RecentRequests(ctx, connectConsoleRecent)
	if err != nil {
		snap.Notes = append(snap.Notes, "The latest requests could not be read: "+setup.ErrorText(err))
		return
	}
	for _, r := range recent {
		snap.Requests = append(snap.Requests, console.Request{
			EventID: r.EventID, Trigger: r.Trigger, EventType: r.EventType, ProjectID: r.BucketID, ProjectName: names.projects[r.BucketID],
			URL: r.RecordingURL, State: r.State, Reason: r.Reason, At: r.UpdatedAt,
		})
	}
}

// readConnectConsoleRunner adds the runner: its settings as runner.json has them, its
// work as its state file has it, and, from a connector running with an
// orchestrator, what each worker is doing this moment.
func readConnectConsoleRunner(ctx context.Context, snap *console.Snapshot, stateDir, configPath string, names connectConsoleNames) {
	dir := connectRunnerDir(stateDir)
	config, configErr := runner.LoadConfig(configPath)
	activity, ran, err := runner.ReadActivity(dir)
	if err != nil {
		snap.Notes = append(snap.Notes, "The runner's state could not be read: "+setup.ErrorText(err))
	}
	_, statErr := os.Lstat(configPath)
	if !ran && errors.Is(statErr, os.ErrNotExist) {
		return
	}

	view := &console.Runner{
		Orchestrator: config.Orchestrator, Worker: config.Worker.Over(config.Orchestrator),
		Permissions: string(config.Mode()), MaxWorkers: config.MaxWorkers, KeptMaxWorkers: config.MaxWorkers,
		Turns: activity.Turns, Waiting: activity.Waiting, SetAside: activity.SetAside, Process: activity.Process,
	}
	if configErr != nil {
		view.ConfigError = setup.ErrorText(configErr)
	}
	if view.Orchestrator.Adapter == "" {
		view.Orchestrator = activity.Orchestrator
		view.Worker = config.Worker.Over(activity.Orchestrator)
	}

	// What is live comes from the running connector, when one answers.
	doing := map[string]string{}
	calling, cancel := context.WithTimeout(ctx, 2*time.Second)
	resp, err := runner.Call(calling, runner.SocketPath(dir), runner.Request{Op: runner.OpWorkers}, 2*time.Second)
	cancel()
	if err == nil && resp.Status != nil {
		view.Live = true
		view.Orchestrator, view.Worker, view.MaxWorkers = resp.Status.Orchestrator, resp.Status.Worker, resp.Status.MaxWorkers
		if resp.Status.ConfigError != "" {
			view.ConfigError = resp.Status.ConfigError
		}
		for _, w := range resp.Workers {
			doing[w.ID] = w.Doing
		}
	}
	// A rule's worker is laid over the default in force, which for a running
	// connector is the one it started with, not the file's.
	for _, rule := range config.Rules {
		view.Rules = append(view.Rules, console.Rule{When: names.ruleWords(rule), Worker: rule.Worker.Over(view.Worker)})
	}
	snap.Runner = view
	now, done := connectConsoleWorkers(activity.Workers, view.Live, doing)
	snap.Now, snap.Done = append(snap.Now, now...), append(snap.Done, done...)
}

// connectConsoleWorkers sorts a runner's workers into those at work and the
// latest that finished, newest first. doing is what each is doing this
// moment, by worker id, from a connector that is live; without one, a
// worker that was at work is waiting for the next start.
func connectConsoleWorkers(workers []runner.WorkerActivity, live bool, doing map[string]string) (now, done []runner.WorkerActivity) {
	for _, w := range workers {
		switch w.State {
		case "running", "queued":
			if live {
				w.Doing = doing[w.ID]
			} else {
				w.State = "interrupted"
			}
			now = append(now, w)
		default:
			done = append(done, w)
		}
	}
	// Sorted before the list is cut to its length: a worker is kept for its
	// thread, so an old one may be the one that finished last.
	slices.SortStableFunc(done, func(a, b runner.WorkerActivity) int {
		return connectConsoleEnded(b).Compare(connectConsoleEnded(a))
	})
	return now, done[:min(len(done), connectConsoleRecent)]
}

func connectConsoleEnded(w runner.WorkerActivity) time.Time {
	if w.EndedAt != nil {
		return *w.EndedAt
	}
	return w.CreatedAt
}

// ruleWords is a rule's conditions as a person reads them.
func (n connectConsoleNames) ruleWords(rule runner.Rule) []string {
	var when []string
	if rule.Repo != "" {
		when = append(when, "work in "+rule.Repo)
	}
	if rule.Project != 0 {
		name := n.projects[rule.Project]
		if name == "" {
			name = "project " + strconv.FormatInt(rule.Project, 10)
		}
		when = append(when, "asked in "+name)
	}
	if rule.CardTable != 0 {
		when = append(when, "on card table "+strconv.FormatInt(rule.CardTable, 10))
	}
	if rule.Todolist != 0 {
		when = append(when, "on to-do list "+strconv.FormatInt(rule.Todolist, 10))
	}
	if rule.Within != 0 {
		when = append(when, "anywhere under recording "+strconv.FormatInt(rule.Within, 10))
	}
	if rule.Ping {
		when = append(when, "asked in a Ping")
	}
	return when
}

func (n connectConsoleNames) person(id int64) console.Person {
	return console.Person{ID: id, Name: n.people[id]}
}

func (n connectConsoleNames) persons(ids []int64) []console.Person {
	people := make([]console.Person, 0, len(ids))
	for _, id := range ids {
		people = append(people, n.person(id))
	}
	return people
}

// connectConsoleProjectURL is a project's address in Basecamp.
func connectConsoleProjectURL(accountID string, projectID int64) string {
	return "https://app.basecamp.com/" + accountID + "/projects/" + strconv.FormatInt(projectID, 10)
}

// connectConsoleNamesEvery is how often the names are read again.
const connectConsoleNamesEvery = 15 * time.Minute

// learnNames reads, as the agent, what Basecamp calls the projects and
// people connect.json names by id, now and every so often until ctx ends.
// The console does not wait for it and works without it: ids are shown
// until a name is known.
func (c *connectConsole) learnNames(ctx context.Context) {
	for {
		projects, people, err := c.readNames(ctx)
		c.mu.Lock()
		if err != nil {
			c.names.err = setup.ErrorText(err)
		} else {
			c.names = connectConsoleNames{projects: projects, people: people}
		}
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(connectConsoleNamesEvery):
		}
	}
}

func (c *connectConsole) readNames(ctx context.Context) (projects map[int64]string, people map[int64]string, err error) {
	p, _, _ := c.current()
	file := p.file
	agent, err := verifiedConnectAgent(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	listed, err := listAgentProjects(ctx, agent.client.ForAccount(agent.account))
	if err != nil {
		return nil, nil, err
	}
	projects = make(map[int64]string, len(listed))
	for _, p := range listed {
		projects[p.ID] = p.Name
	}
	ids := append([]int64{file.Agent.PersonID, file.Trust.OperatorID}, file.Trust.AllowlistIDs...)
	ids = append(ids, file.Trust.AgentIDs...)
	for _, trust := range file.Pings {
		ids = append(ids, trust.Participants...)
	}
	people = map[int64]string{}
	for _, id := range ids {
		if _, done := people[id]; done || id <= 0 {
			continue
		}
		// One person who cannot be read is shown by id; the rest still have
		// their names.
		if person, err := agent.reader.Person(ctx, id); err == nil {
			people[id] = person.Name
		}
	}
	return projects, people, nil
}
