package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// filingContainer is a kind of project tool that the web's Move… and Copy…
// dialogs file recordings into, such as a message board, and how a command
// names one: the project it is in (--to-project), which of that project's
// tools of the kind (flag, by ID or title), or its ID or URL alone.
type filingContainer struct {
	// dockName is the tool's name in a project's dock: "message_board".
	dockName string
	noun     string
	// plural names several of them in messages: "message boards", or
	// "To-dos tools" where appending "s" reads wrong. Every container sets it.
	plural   string
	flag     string
	urlKind  string
	urlShape string
	// get reads one by ID, for a destination named without --to-project.
	get func(ctx context.Context, app *appctx.App, id int64) (filingTarget, error)
}

// filingTarget is the container a recording is moved or copied into.
type filingTarget struct {
	id    int64
	title string
	// appURL is where a filed recording lands, in Basecamp: the folder when
	// parentID is set.
	appURL      string
	projectID   int64
	projectName string
	// calendar is set when the container is in a calendar, such as an
	// employee's, rather than a project.
	calendar bool
	// parentID is a folder inside a Docs & Files destination, or 0 for its
	// top level.
	parentID    int64
	parentTitle string
}

// describe names the target for messages: "message board 900 (Ideas) in Dest".
func (t filingTarget) describe(noun string) string {
	place := fmt.Sprintf("%s %d", noun, t.id)
	if t.title != "" {
		place += " (" + t.title + ")"
	}
	if t.parentID != 0 {
		place = fmt.Sprintf("folder %d (%s) in %s", t.parentID, t.parentTitle, place)
	}
	if t.projectName != "" {
		place += " in " + t.projectName
	}
	return place
}

// resolveFilingTarget finds the container named by --to-project and the
// container's own flag: an ID or URL on its own, a project's only enabled
// tool of the kind, or one of them picked by ID or title. Only enabled tools
// on the project's dock count, as in the web's dialogs. A URL is held to
// parseFilingRef's host and account checks, and its project must agree with
// --to-project.
func resolveFilingTarget(cmd *cobra.Command, app *appctx.App, c filingContainer, toProject, toContainer string) (filingTarget, error) {
	var urlProjectID, url string
	if urlarg.IsURL(toContainer) {
		ref, err := parseFilingRef(app, toContainer, c.noun, c.urlKind, c.urlShape)
		if err != nil {
			return filingTarget{}, err
		}
		url = toContainer
		toContainer, urlProjectID = strconv.FormatInt(ref.id, 10), ref.project
	}

	if toProject == "" {
		id, err := strconv.ParseInt(toContainer, 10, 64)
		if err != nil || id <= 0 {
			return filingTarget{}, output.ErrUsage(fmt.Sprintf("%s must be a positive %s ID", c.flag, c.noun))
		}
		target, err := c.get(cmd.Context(), app, id)
		if err != nil {
			return filingTarget{}, convertSDKError(err)
		}
		if urlProjectID != "" && !sameNumericID(urlProjectID, strconv.FormatInt(target.projectID, 10)) {
			return filingTarget{}, output.ErrUsage(fmt.Sprintf("The %s URL names project %s, but %s %d is in project %d", c.flag, urlProjectID, c.noun, id, target.projectID))
		}
		return target, nil
	}

	projectID, projectName, err := app.Names.ResolveProject(cmd.Context(), toProject)
	if err != nil {
		return filingTarget{}, err
	}
	if urlProjectID != "" && !sameNumericID(urlProjectID, projectID) {
		return filingTarget{}, output.ErrUsageHint(
			fmt.Sprintf("The %s URL is in project %s, but --to-project points at project %s", c.flag, urlProjectID, projectID),
			fmt.Sprintf("Drop --to-project, since the %s URL names its own project", c.noun),
		)
	}
	enabled, all, err := getDockTools(cmd.Context(), app, projectID, c.dockName)
	if err != nil {
		return filingTarget{}, err
	}
	projectName = projectLabel(cmd, app, projectID, projectName)
	pick := func(tool DockTool) filingTarget {
		id, _ := strconv.ParseInt(projectID, 10, 64)
		return filingTarget{id: tool.ID, title: tool.Title, appURL: tool.AppURL, projectID: id, projectName: projectName}
	}

	if toContainer == "" {
		switch len(enabled) {
		case 0:
			return filingTarget{}, noFilingContainerError(c, all, projectID, projectName)
		case 1:
			return pick(enabled[0]), nil
		default:
			return filingTarget{}, ambiguousFilingContainerError(c, projectName, enabled)
		}
	}
	var named []DockTool
	for _, tool := range enabled {
		if strconv.FormatInt(tool.ID, 10) == toContainer {
			return pick(tool), nil
		}
		if url == "" && strings.EqualFold(tool.Title, toContainer) {
			named = append(named, tool)
		}
	}
	if url != "" {
		return filingTarget{}, filingURLNotFound(c.flag, c.noun, url, projectName,
			fmt.Sprintf("Its %s: %s", c.plural, formatDockTools(enabled)))
	}
	switch len(named) {
	case 0:
	case 1:
		return pick(named[0]), nil
	default:
		return filingTarget{}, ambiguousFilingContainerError(c, projectName, named)
	}
	if len(enabled) == 0 {
		return filingTarget{}, noFilingContainerError(c, all, projectID, projectName)
	}
	return filingTarget{}, output.ErrUsageHint(
		fmt.Sprintf("%s %q is not a %s in %s", c.flag, toContainer, c.noun, projectName),
		fmt.Sprintf("Its %s: %s", c.plural, formatDockTools(enabled)),
	)
}

// bucketTarget is a container read by ID, in the project its bucket names.
func bucketTarget(id int64, title, appURL string, bucket *basecamp.Bucket) filingTarget {
	target := filingTarget{id: id, title: title, appURL: appURL}
	if bucket != nil {
		target.projectID, target.projectName = bucket.ID, bucket.Name
		target.calendar = bucket.Type == "Calendar"
	}
	return target
}

// filingURLNotFound refuses a destination URL that names no container, in
// project when it is set. A URL carries an id, never a title, so it is not
// looked up as one: a flat URL names no project, and its id ("2026") can be a
// folder's or board's title.
func filingURLNotFound(flag, noun, url, project, hint string) error {
	message := fmt.Sprintf("%s %s names no %s", flag, url, noun)
	if project != "" {
		message += " in " + project
	}
	return &output.Error{Code: output.CodeNotFound, Message: message, Hint: hint}
}

// noFilingContainerError says the project has no enabled tool of the kind,
// and how to turn one on when it is only disabled.
func noFilingContainerError(c filingContainer, all []DockTool, projectID, projectName string) error {
	for _, tool := range all {
		if !tool.Enabled {
			return &output.Error{
				Code:    output.CodeNotFound,
				Message: fmt.Sprintf("%s's %s (%s) is turned off, so nothing can be moved or copied into it", projectName, c.noun, tool.Title),
				Hint:    fmt.Sprintf("Turn it on first with: basecamp tools enable %d --in %s", tool.ID, projectID),
			}
		}
	}
	return &output.Error{
		Code:    output.CodeNotFound,
		Message: fmt.Sprintf("Project %s has no %s", projectName, c.noun),
		Hint:    fmt.Sprintf("Pick a project that has a %s", c.noun),
	}
}

func ambiguousFilingContainerError(c filingContainer, projectName string, tools []DockTool) error {
	lines := make([]string, 0, len(tools))
	for _, tool := range tools {
		lines = append(lines, fmt.Sprintf("  %d  %s", tool.ID, tool.Title))
	}
	return &output.Error{
		Code:    output.CodeAmbiguous,
		Message: fmt.Sprintf("%s has %d %s that match", projectName, len(tools), c.plural),
		Hint:    fmt.Sprintf("Specify one with %s <id>:\n%s", c.flag, strings.Join(lines, "\n")),
	}
}

func formatDockTools(tools []DockTool) string {
	if len(tools) == 0 {
		return "none"
	}
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = fmt.Sprintf("%d (%s)", tool.ID, tool.Title)
	}
	return strings.Join(names, ", ")
}

// filingSource is a recording to move or copy, as its own read returns it.
type filingSource struct {
	id     int64
	typ    string
	status string
	title  string
	bucket *basecamp.Bucket
	parent *basecamp.Parent
}

// filingFamily is one kind of recording the web moves and copies on its own,
// such as messages, with what its move and copy commands need to know.
type filingFamily struct {
	noun string
	// group is the command group that shows and moves one: "basecamp messages".
	group    string
	types    []string
	urlKinds []string
	urlShape string
	into     filingContainer
	// get reads one by ID: the record to output, and what filing needs.
	get func(ctx context.Context, app *appctx.App, id int64) (any, filingSource, error)
	// check refuses a recording Basecamp will not file; nil checks the type.
	check func(src filingSource) error
	// resolve finds the destination; nil uses resolveFilingTarget.
	resolve func(cmd *cobra.Command, app *appctx.App, toProject, toContainer string) (filingTarget, error)
	// own is the container the recording is in now, for a copy with no
	// destination; nil reads the parent as an into container.
	own func(ctx context.Context, app *appctx.App, src filingSource) (filingTarget, error)
	// alreadyThere refuses moving a recording onto its own container; nil
	// compares the parent with the target.
	alreadyThere func(src filingSource, target filingTarget) error
	// sameHint is the same-project command for a move onto the recording's
	// own container, given its ID; empty when there is none.
	sameHint func(src filingSource, target filingTarget) string
	// crumbs are the follow-ups for a filed recording.
	crumbs func(id, projectID int64) []output.Breadcrumb
}

// filingHelp is a move or copy command's help.
type filingHelp struct {
	use, short, long, notes string
}

func (fam filingFamily) showCmd() string { return fam.group + " show" }

// newFilingCmd makes a family's move or copy command.
func newFilingCmd(fam filingFamily, kind basecamp.FilingType, help filingHelp, bulk bool) *cobra.Command {
	var toProject, toContainer string
	var includeComments bool

	verb := "Move"
	if kind == basecamp.FilingCopy {
		verb = "Copy"
	}
	args := cobra.ExactArgs(1)
	if bulk {
		args = cobra.MinimumNArgs(1)
	}
	cmd := &cobra.Command{
		Use:         help.use,
		Short:       help.short,
		Long:        help.long,
		Args:        args,
		Annotations: map[string]string{"agent_notes": help.notes},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFamilyFiling(cmd, fam, kind, args, toProject, toContainer, includeComments)
		},
	}
	addFamilyFilingFlags(cmd, fam, verb, &toProject, &toContainer)
	if kind == basecamp.FilingCopy {
		cmd.Flags().BoolVar(&includeComments, "include-comments", false, "Copy the "+fam.noun+"'s comments too")
	}
	return guardFilingFlags(cmd)
}

func addFamilyFilingFlags(cmd *cobra.Command, fam filingFamily, verb string, toProject, toContainer *string) {
	cmd.Flags().StringVar(toProject, "to-project", "", fmt.Sprintf("%s it into this project's %s (ID or name)", verb, fam.into.noun))
	cmd.Flags().StringVar(toContainer, strings.TrimPrefix(fam.into.flag, "--"), "", fmt.Sprintf("%s it into this %s (ID, URL, or a name with --to-project)", verb, fam.into.noun))
}

// familyFilingSet reports whether a destination flag was given.
func familyFilingSet(cmd *cobra.Command, fam filingFamily) bool {
	return cmd.Flags().Changed("to-project") || cmd.Flags().Changed(strings.TrimPrefix(fam.into.flag, "--"))
}

// runFamilyFiling moves or copies each recording in args into the destination
// the flags name, one filing each. Every recording is read before any is
// filed, so a bad ID, a recording named twice or one already in the
// destination stops the command before anything moves.
func runFamilyFiling(cmd *cobra.Command, fam filingFamily, kind basecamp.FilingType, args []string, toProject, toContainer string, includeComments bool) error {
	filingSet := familyFilingSet(cmd, fam)
	if kind == basecamp.FilingMove && !filingSet {
		return missingArg(cmd, "--to-project")
	}
	if filingSet {
		if toProject == "" && toContainer == "" {
			return output.ErrUsage(fmt.Sprintf("--to-project or %s needs a value", fam.into.flag))
		}
		if toProject == "" && !isNumericID(toContainer) && !urlarg.IsURL(toContainer) {
			return output.ErrUsageHint(
				fmt.Sprintf("%s %q is a name, which needs a project to look it up in", fam.into.flag, toContainer),
				fmt.Sprintf("Add --to-project <project>, or pass the %s's ID or URL", fam.into.noun),
			)
		}
	}

	app := appctx.FromContext(cmd.Context())
	scope := newFollowUpScope(app)
	if err := ensureAccount(cmd, app); err != nil {
		return err
	}

	refs := make([]filesMoveRef, 0, len(args))
	for _, arg := range args {
		ref, err := parseFilingRefOf(app, arg, fam.noun, fam.urlShape, fam.urlKinds)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}

	var target filingTarget
	if filingSet {
		var err error
		if fam.resolve != nil {
			target, err = fam.resolve(cmd, app, toProject, toContainer)
		} else {
			target, err = resolveFilingTarget(cmd, app, fam.into, toProject, toContainer)
		}
		if err != nil {
			return err
		}
	}

	filings := make([]recordingFiling, 0, len(refs))
	named := filingNames{}
	for i, ref := range refs {
		_, src, err := fam.get(cmd.Context(), app, ref.id)
		if err != nil {
			return convertSDKError(err)
		}
		if err := fam.checkSource(src, ref.id); err != nil {
			return err
		}
		if err := named.add(fam.noun, args[i], src.id); err != nil {
			return err
		}
		if err := rejectProjectConflict(cmd, app, fam.noun, flagValue(cmd, "project"), ref.project, src.bucket.ID); err != nil {
			return err
		}
		dest := target
		if !filingSet {
			if dest, err = fam.ownContainer(cmd.Context(), app, src); err != nil {
				return err
			}
		}
		if kind == basecamp.FilingMove {
			if err := fam.refuseOwnContainer(src, dest); err != nil {
				return err
			}
		}
		filings = append(filings, fam.filing(app, kind, src, dest, includeComments))
	}
	return runFilings(cmd, app, scope, filings)
}

// flagValue reads a flag the command may inherit, or "" when it has none.
func flagValue(cmd *cobra.Command, name string) string {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f.Value.String()
	}
	return ""
}

func (fam filingFamily) checkSource(src filingSource, requestedID int64) error {
	if src.bucket == nil {
		return output.ErrNotFound(fam.noun, strconv.FormatInt(requestedID, 10))
	}
	// A moved recording's old id redirects to where it went, which may be
	// read through another kind's endpoint, so the read alone does not prove
	// the id names one of these.
	if src.typ != "" && !slices.Contains(fam.types, src.typ) {
		return output.ErrUsage(fmt.Sprintf("%d is a %s, not a %s", requestedID, src.typ, fam.noun))
	}
	if fam.check != nil {
		return fam.check(src)
	}
	return nil
}

func (fam filingFamily) ownContainer(ctx context.Context, app *appctx.App, src filingSource) (filingTarget, error) {
	if fam.own != nil {
		return fam.own(ctx, app, src)
	}
	if src.parent == nil {
		return filingTarget{}, output.ErrUsageHint(
			fmt.Sprintf("Could not tell which %s %s #%d is in", fam.into.noun, fam.noun, src.id),
			fmt.Sprintf("Name the destination with --to-project <project> or %s <id|url>", fam.into.flag),
		)
	}
	target, err := fam.into.get(ctx, app, src.parent.ID)
	if err != nil {
		return filingTarget{}, convertSDKError(err)
	}
	return target, nil
}

func (fam filingFamily) refuseOwnContainer(src filingSource, target filingTarget) error {
	if fam.alreadyThere != nil {
		return fam.alreadyThere(src, target)
	}
	if src.parent == nil || src.parent.ID != target.id {
		return nil
	}
	e := output.ErrUsage(fmt.Sprintf("%s #%d is already in %s", capitalizeFirst(fam.noun), src.id, target.describe(fam.into.noun)))
	if fam.sameHint != nil {
		e.Hint = fam.sameHint(src, target)
	} else {
		e.Hint = fmt.Sprintf("Pick another %s with --to-project <project> or %s <id|url>", fam.into.noun, fam.into.flag)
	}
	return e
}

// filing is a Move… or Copy… of src into target, read back by the family's
// own read.
func (fam filingFamily) filing(app *appctx.App, kind basecamp.FilingType, src filingSource, target filingTarget, includeComments bool) recordingFiling {
	return recordingFiling{
		kind:            kind,
		sourceID:        src.id,
		noun:            fam.noun,
		destinationID:   target.id,
		parentID:        target.parentID,
		includeComments: includeComments,
		destination:     target.describe(fam.into.noun),
		showCmd:         fam.showCmd(),
		lookFor:         copyLookFor(src, target),
		read: func(ctx context.Context, id int64) (filedRecording, error) {
			record, filed, err := fam.get(ctx, app, id)
			if err != nil {
				return filedRecording{}, err
			}
			var projectID int64
			if filed.bucket != nil {
				projectID = filed.bucket.ID
			}
			return filedRecording{
				result:      withSourceID(record, src.id),
				id:          filed.id,
				bucket:      filed.bucket,
				parent:      filed.parent,
				breadcrumbs: fam.crumbs(filed.id, projectID),
			}, nil
		},
	}
}

// withSourceID adds source_id, the recording a filing came from, to a filed
// recording's JSON.
func withSourceID(record any, sourceID int64) any {
	raw, err := json.Marshal(record)
	if err != nil {
		return record
	}
	fields := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return record
	}
	fields["source_id"] = sourceID
	return fields
}
