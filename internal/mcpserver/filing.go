package mcpserver

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/basecamp/mcp/catalog"
	"github.com/basecamp/mcp/gateway"
)

// Filing actions: recordings.move and recordings.copy file a recording into
// another container, in its project or another one, the way the web's Move…
// and Copy… dialogs do. Basecamp files in the background, so each action
// starts the filing once through RecordingsService.MoveAndWait or
// CopyAndWait, waits for Basecamp to finish it, and answers with the new
// recording read back at its new id. They replace the model's start-only
// MoveRecording and CopyRecording, which answered with a filing to poll.
// GetFiling moves into the recordings domain beside them, as the check for a
// filing that had not finished when the wait ended.
const (
	moveAction = "move"
	copyAction = "copy"
)

// filingWait bounds how long move and copy wait for Basecamp to finish: the
// SDK's default minute. Tests shorten it.
var filingWait = basecamp.FilingWaitOptions{}

// recordingRead is one recording as its typed read returns it: the record
// to answer with, and the fields filing needs from it.
type recordingRead struct {
	record json.RawMessage
	id     int64
	typ    string
	parent *basecamp.Parent
}

type recordingReader func(ctx context.Context, api API, id int64) (recordingRead, error)

// filingReads holds the typed read for each recordable type Basecamp moves
// and copies on its own: every type basecamp.FilingContainerFor accepts. A
// to-do list and a to-do list group are both Todolist, and a folder is a
// Vault; FilingContainerFor tells them apart by the parent the read reports.
var filingReads = map[string]recordingReader{
	"Kanban::Card":    reader(API.Cards, (*basecamp.CardsService).Get),
	"Kanban::Column":  reader(API.CardColumns, (*basecamp.CardColumnsService).Get),
	"Message":         reader(API.Messages, (*basecamp.MessagesService).Get),
	"Document":        reader(API.Documents, (*basecamp.DocumentsService).Get),
	"Upload":          reader(API.Uploads, (*basecamp.UploadsService).Get),
	"CloudFile":       reader(API.CloudFiles, (*basecamp.CloudFilesService).Get),
	"GoogleDocument":  reader(API.GoogleDocuments, (*basecamp.GoogleDocumentsService).Get),
	"Vault":           reader(API.Vaults, (*basecamp.VaultsService).Get),
	"Schedule::Entry": seriesReader(reader(API.Schedules, (*basecamp.SchedulesService).GetEntry)),
	"Question":        reader(API.Checkins, (*basecamp.CheckinsService).GetQuestion),
	"Inbox::Forward":  reader(API.Forwards, (*basecamp.ForwardsService).Get),
	"Todolist":        reader(API.Todolists, (*basecamp.TodolistsService).Get),
	"Todo":            reader(API.Todos, (*basecamp.TodosService).Get),
}

// reader adapts one SDK typed read into a recordingReader. The record goes
// back to the caller as the SDK serializes it, so the answer has the same
// shape as that type's own get action.
func reader[S, T any](service func(API) S, get func(S, context.Context, int64) (*T, error)) recordingReader {
	return func(ctx context.Context, api API, id int64) (recordingRead, error) {
		record, err := get(service(api), ctx, id)
		if err != nil {
			return recordingRead{}, err
		}
		raw, err := json.Marshal(record)
		if err != nil {
			return recordingRead{}, err
		}
		var fields struct {
			ID     int64            `json:"id"`
			Type   string           `json:"type"`
			Parent *basecamp.Parent `json:"parent"`
		}
		if err := json.Unmarshal(raw, &fields); err != nil {
			return recordingRead{}, err
		}
		return recordingRead{record: raw, id: fields.ID, typ: fields.Type, parent: fields.Parent}, nil
	}
}

// occurrenceType is what Basecamp answers a recurring event's read with: it
// redirects to one occurrence, whose parent is the event.
const occurrenceType = "Schedule::Entry::Occurrence"

// seriesReader reads a recurring event as its series, which is what files: the
// read names the event, with the occurrence it answered with kept as
// occurrence_id. An occurrence's parent is the event, not the schedule, so the
// record carries no parent.
func seriesReader(read recordingReader) recordingReader {
	return func(ctx context.Context, api API, id int64) (recordingRead, error) {
		r, err := read(ctx, api, id)
		if err != nil || r.typ != occurrenceType || r.parent == nil {
			return r, err
		}
		fields := map[string]any{}
		decoder := json.NewDecoder(bytes.NewReader(r.record))
		decoder.UseNumber()
		if err := decoder.Decode(&fields); err != nil {
			return recordingRead{}, err
		}
		fields["occurrence_id"] = fields["id"]
		fields["id"] = r.parent.ID
		fields["type"] = r.parent.Type
		delete(fields, "parent")
		raw, err := json.Marshal(fields)
		if err != nil {
			return recordingRead{}, err
		}
		return recordingRead{record: raw, id: r.parent.ID, typ: r.parent.Type}, nil
	}
}

func filingTypes() []any {
	types := make([]any, 0, len(filingReads))
	for _, typ := range slices.Sorted(maps.Keys(filingReads)) {
		types = append(types, typ)
	}
	return types
}

// containerNouns names each kind of container in messages.
var containerNouns = map[basecamp.FilingContainer]string{
	basecamp.FilingIntoCardTable:     "card table",
	basecamp.FilingIntoMessageBoard:  "message board",
	basecamp.FilingIntoVault:         "Docs & Files",
	basecamp.FilingIntoSchedule:      "schedule",
	basecamp.FilingIntoQuestionnaire: "Automatic Check-ins",
	basecamp.FilingIntoInbox:         "Email Forwards",
	basecamp.FilingIntoTodoset:       "To-dos",
	basecamp.FilingIntoTodolist:      "to-do list",
}

func filingOperation(kind basecamp.FilingType) *catalog.Operation {
	verb, verbs, action, id := "Move", "Moves", moveAction, "MoveRecordingAndWait"
	if kind == basecamp.FilingCopy {
		verb, verbs, action, id = "Copy", "Copies", copyAction, "CopyRecordingAndWait"
	}
	lower := strings.ToLower(verb)
	properties := map[string]any{
		"recording_id": idSchema("The recording to " + lower + "."),
		"recording_type": map[string]any{
			"type":        "string",
			"enum":        filingTypes(),
			"description": "The recording's type as Basecamp spells it (its JSON \"type\"). A to-do list group is a Todolist and a folder is a Vault.",
		},
		"project_id":   idSchema("The project to " + lower + " it into. Its one enabled tool of the recording's kind is the destination: its card table, message board, Docs & Files, schedule, Automatic Check-ins or Email Forwards, or for a to-do or to-do list group its one to-do list. A project with none, or several, is refused with what to pass instead. Pass project_id or container_id, not both."),
		"container_id": idSchema("The container to " + lower + " it into, of the recording's kind: a card table, message board, Docs & Files tool, schedule, Automatic Check-ins, Email Forwards or To-dos tool from a project's dock, or a to-do list for a to-do or to-do list group. A folder is not a container; pass its Docs & Files here and the folder as folder_id."),
		"folder_id":    idSchema("A folder inside the destination Docs & Files to put it in. Only for documents, uploads, cloud files, Google documents and folders."),
	}
	doc := verbs + " a recording into another container, in its project or another one, as the web's " + verb + "… dialog does. " +
		"Composed in basecamp-sdk (RecordingsService." + verb + "AndWait): it reads the recording, resolves the destination, starts the " + lower + " once, waits up to a minute for Basecamp to finish it, and reads back the new recording. Not backed by any single Basecamp endpoint, so this action carries no method and no path.\n\n" +
		"Returns the new recording, as its type's own get action returns it, with source_id naming the recording it came from. A " + lower + " always gives the recording a new id. A card lands in the card table's Triage, or Done if it is completed.\n\n" +
		"Comments, card steps, check-in answers and other recordings that go with their parent are not filed on their own, and neither is a whole tool (a project's Docs & Files or card table itself).\n\n" +
		"A failure says how far it got in error.type, and error.retry_safe says whether calling this action again is safe (a malformed call is refused as plain text instead). recording_unreadable and destination_unreadable are a read before the filing that failed, carrying the read's own error. recording_moved is an id that now redirects to recording_id, because the recording was moved: nothing was filed, which is how a retried move is refused once the first one landed. destination_missing, destination_disabled and destination_ambiguous filed nothing and name what to pass instead (destination_disabled carries the tool_id that basecamp_automation enable_tool turns on; destination_ambiguous lists candidates for container_id). filing_refused filed nothing. filing_unsettled was still running when the wait ended: do not " + lower + " again; check it with get_filing and the filing_id it carries. filing_failed was started and reported failed by Basecamp. filed_unread was filed under recording_id but could not be read back."
	if kind == basecamp.FilingCopy {
		properties["include_comments"] = map[string]any{
			"type":        "boolean",
			"description": "Copy the recording's comments too. Defaults to false.",
		}
		doc += " filing_may_have_started is a start that failed without Basecamp refusing it: a copy may exist, and copying again makes another, so look for it in the destination first."
	} else {
		doc += " filing_may_have_started is a start that failed without Basecamp refusing it; Basecamp refuses a repeated move, so trying again is safe."
	}
	return &catalog.Operation{
		ID:           id,
		Action:       action,
		Tag:          recordingsDomainTag,
		Summary:      syntheticSummaryTag + lower + " a recording into another container or project, wait for Basecamp to finish, and return the new recording.",
		Doc:          doc,
		BodyRequired: true,
		Body: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"recording_id", "recording_type"},
			"properties":           properties,
		},
	}
}

// installFilingOperations replaces the model's start-only MoveRecording and
// CopyRecording with the waiting composites, and moves GetFiling into the
// recordings domain beside them. Each must be where the model put it: a
// sync that renamed or dropped one stops the load instead of serving a
// stale twin or losing the check for an unsettled filing.
func installFilingOperations(cat *catalog.Catalog, recordings *catalog.Domain) error {
	for _, id := range []string{"MoveRecording", "CopyRecording"} {
		if _, err := takeOperation(cat, id); err != nil {
			return err
		}
	}
	getFiling, err := takeOperation(cat, "GetFiling")
	if err != nil {
		return err
	}
	getFiling.Summary = "Get the state of a move or copy, by the filing_id a move or copy answered with when it had not finished"
	recordings.Operations = append(recordings.Operations, getFiling)
	slices.SortFunc(recordings.Operations, func(a, b *catalog.Operation) int { return strings.Compare(a.Action, b.Action) })
	return nil
}

func takeOperation(cat *catalog.Catalog, id string) (*catalog.Operation, error) {
	for _, d := range cat.Domains {
		for i, op := range d.Operations {
			if op.ID == id {
				d.Operations = slices.Delete(d.Operations, i, i+1)
				return op, nil
			}
		}
	}
	return nil, fmt.Errorf("the model serves no %q operation for the filing actions to take over", id)
}

// filingRequest is one move or copy as the caller named it.
type filingRequest struct {
	kind            basecamp.FilingType
	recordingID     int64
	recordingType   string
	projectID       int64
	containerID     int64
	folderID        int64
	includeComments bool
}

func handleFiling(kind basecamp.FilingType) compositeHandler {
	return func(ctx context.Context, api API, op *catalog.Operation, params map[string]any) (*mcp.CallToolResult, error) {
		req, err := parseFilingRequest(kind, op, params)
		if err != nil {
			return gateway.ErrorResult("%v", err), nil
		}
		answer, err := fileRecording(ctx, api, req)
		if err != nil {
			if failure, ok := errors.AsType[*filingError](err); ok {
				return failure.result(), nil
			}
			return gateway.ErrorResult("%v", err), nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(answer)}}}, nil
	}
}

func parseFilingRequest(kind basecamp.FilingType, op *catalog.Operation, params map[string]any) (filingRequest, error) {
	req := filingRequest{kind: kind}
	if err := refuseUnknownParams(op, params); err != nil {
		return req, err
	}
	var err error
	if req.recordingID, err = requiredID(params, "recording_id"); err != nil {
		return req, err
	}
	if req.recordingType, err = optionalString(params, "recording_type"); err != nil {
		return req, err
	}
	if _, ok := filingReads[req.recordingType]; !ok {
		return req, fmt.Errorf("parameter %q must be one of the types Basecamp moves and copies on its own (describe the action for the list), got %q", "recording_type", req.recordingType)
	}
	for name, field := range map[string]*int64{"project_id": &req.projectID, "container_id": &req.containerID, "folder_id": &req.folderID} {
		if _, given := params[name]; given {
			if *field, err = requiredID(params, name); err != nil {
				return req, err
			}
		}
	}
	if (req.projectID == 0) == (req.containerID == 0) {
		return req, errors.New("pass exactly one of project_id and container_id to name where it goes")
	}
	if raw, given := params["include_comments"]; given && raw != nil {
		include, ok := raw.(bool)
		if !ok {
			return req, fmt.Errorf("parameter %q must be a boolean, got %T", "include_comments", raw)
		}
		req.includeComments = include
	}
	return req, nil
}

// fileRecording reads the recording, resolves where it goes, files it once
// and waits, then reads back what Basecamp made.
func fileRecording(ctx context.Context, api API, req filingRequest) (json.RawMessage, error) {
	src, err := filingReads[req.recordingType](ctx, api, req.recordingID)
	if err != nil {
		return nil, unreadable("recording_unreadable", fmt.Sprintf("Could not read %s %d", req.recordingType, req.recordingID), err)
	}
	if src.id != req.recordingID {
		// A moved recording's old id redirects to its new one. Filing what
		// the redirect found would move a recording again when a lost answer
		// to a move is retried, so the caller names it by its new id.
		return nil, &filingError{
			Type:        "recording_moved",
			Message:     fmt.Sprintf("Recording %d has been moved and is now %d; nothing was filed. Pass recording_id %d to file it from where it is now", req.recordingID, src.id, src.id),
			RetrySafe:   true,
			RecordingID: src.id,
		}
	}
	if src.typ != req.recordingType {
		return nil, fmt.Errorf("recording %d is a %s, not a %s", req.recordingID, src.typ, req.recordingType)
	}
	var parentType string
	if src.parent != nil {
		parentType = src.parent.Type
	}
	container, ok := basecamp.FilingContainerFor(src.typ, parentType)
	if !ok {
		return nil, &filingError{
			Type:      "not_filable",
			Message:   fmt.Sprintf("%s %d, whose parent is a %s, is not filed on its own: this action files the recordings inside a tool, not a whole tool such as a project's Docs & Files", src.typ, src.id, cmp.Or(parentType, "project")),
			RetrySafe: true,
		}
	}
	if req.folderID != 0 && !container.Folders() {
		return nil, fmt.Errorf("folder_id applies only to Docs & Files items; a %s goes into a %s", src.typ, containerNouns[container])
	}

	destinationID := req.containerID
	if destinationID == 0 {
		if destinationID, err = destinationInProject(ctx, api, container, req.projectID); err != nil {
			return nil, err
		}
	}

	recordings := api.Recordings()
	var filing *basecamp.Filing
	if req.kind == basecamp.FilingCopy {
		filing, err = recordings.CopyAndWait(ctx, src.id, &basecamp.CopyRecordingRequest{
			FilingDestinationID: destinationID,
			ParentID:            req.folderID,
			IncludeComments:     req.includeComments,
		}, &filingWait)
	} else {
		filing, err = recordings.MoveAndWait(ctx, src.id, &basecamp.MoveRecordingRequest{
			FilingDestinationID: destinationID,
			ParentID:            req.folderID,
		}, &filingWait)
	}
	if err != nil {
		return nil, explainFilingError(req, container, err)
	}

	made := filing.DestinationRecording
	read, ok := filingReads[made.Type]
	if !ok {
		read = filingReads[src.typ]
	}
	filed, err := read(ctx, api, made.ID)
	if err != nil {
		return nil, &filingError{
			Type:        "filed_unread",
			Message:     fmt.Sprintf("%s %d as %s %d, but could not read it back: %v", pastTense(req.kind), src.id, made.Type, made.ID, err),
			RecordingID: made.ID,
			AppURL:      made.AppURL,
		}
	}
	return withSourceID(filed.record, src.id)
}

// destinationInProject finds the one container of the kind in a project: its
// enabled dock tool of that name, or for a to-do list, its To-dos tool's one
// list. Only enabled tools count, as in the web's dialogs.
func destinationInProject(ctx context.Context, api API, container basecamp.FilingContainer, projectID int64) (int64, error) {
	project, err := api.Projects().Get(ctx, projectID)
	if err != nil {
		return 0, unreadable("destination_unreadable", fmt.Sprintf("Could not read project %d", projectID), err)
	}
	toolKind := container
	if container == basecamp.FilingIntoTodolist {
		toolKind = basecamp.FilingIntoTodoset
	}
	var enabled []basecamp.DockItem
	var disabled *basecamp.DockItem
	for _, tool := range project.Dock {
		switch {
		case tool.Name != string(toolKind):
		case tool.Enabled:
			enabled = append(enabled, tool)
		case disabled == nil:
			disabled = &tool
		}
	}
	noun := containerNouns[toolKind]
	switch {
	case len(enabled) == 0 && disabled != nil:
		return 0, &filingError{
			Type:      "destination_disabled",
			Message:   fmt.Sprintf("Project %s's %s (%s) is turned off, so nothing can be moved or copied into it. Turn it on with basecamp_automation enable_tool and toolId %d, or pick another project", project.Name, noun, disabled.Title, disabled.ID),
			RetrySafe: true,
			ToolID:    disabled.ID,
		}
	case len(enabled) == 0:
		return 0, &filingError{
			Type:      "destination_missing",
			Message:   fmt.Sprintf("Project %s has no %s", project.Name, noun),
			RetrySafe: true,
		}
	}

	// The candidates are what container_id takes: the tools themselves, or
	// for a to-do list, the lists in every enabled To-dos tool.
	candidates := make([]filingCandidate, 0, len(enabled))
	plural := noun + " tools"
	if container == basecamp.FilingIntoTodolist {
		plural = "to-do lists"
		for _, tool := range enabled {
			lists, err := api.Todolists().List(ctx, tool.ID, nil)
			if err != nil {
				return 0, unreadable("destination_unreadable", fmt.Sprintf("Could not list the to-do lists in project %s's %s", project.Name, tool.Title), err)
			}
			for _, list := range lists.Todolists {
				candidates = append(candidates, filingCandidate{ID: list.ID, Title: list.Title})
			}
		}
	} else {
		for _, tool := range enabled {
			candidates = append(candidates, filingCandidate{ID: tool.ID, Title: tool.Title})
		}
	}
	switch len(candidates) {
	case 0:
		return 0, &filingError{
			Type:      "destination_missing",
			Message:   fmt.Sprintf("Project %s has no %s", project.Name, plural),
			RetrySafe: true,
		}
	case 1:
		return candidates[0].ID, nil
	}
	return 0, &filingError{
		Type:       "destination_ambiguous",
		Message:    fmt.Sprintf("Project %s has %d %s; pass one of them as container_id", project.Name, len(candidates), plural),
		RetrySafe:  true,
		Candidates: candidates,
	}
}

// filingError is a move or copy that did not end with a new recording read
// back: how far it got, and whether calling the action again is safe.
type filingError struct {
	Type         string            `json:"type"`
	Message      string            `json:"message"`
	RetrySafe    bool              `json:"retry_safe"`
	FilingID     int64             `json:"filing_id,omitempty"`
	FilingStatus string            `json:"filing_status,omitempty"`
	RecordingID  int64             `json:"recording_id,omitempty"`
	AppURL       string            `json:"app_url,omitempty"`
	ToolID       int64             `json:"tool_id,omitempty"`
	Candidates   []filingCandidate `json:"candidates,omitempty"`
}

type filingCandidate struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
}

func (f *filingError) Error() string { return f.Message }

// unreadable is a read before the filing that failed: nothing was filed, so
// trying again is safe, and the read's own error says whether it can work.
func unreadable(kind, what string, err error) *filingError {
	return &filingError{Type: kind, Message: fmt.Sprintf("%s, so nothing was filed: %v", what, err), RetrySafe: true}
}

func (f *filingError) result() *mcp.CallToolResult {
	result, _ := gateway.JSONResult(map[string]any{"error": f})
	result.IsError = true
	return result
}

// explainFilingError says how far a failed MoveAndWait or CopyAndWait got.
func explainFilingError(req filingRequest, container basecamp.FilingContainer, err error) error {
	verb := strings.ToLower(string(req.kind))
	if refused, ok := errors.AsType[*basecamp.FilingRefusedError](err); ok {
		message := fmt.Sprintf("Basecamp refused to %s it, and nothing was filed: %v", verb, refused.Err)
		if apiErr, ok := errors.AsType[*basecamp.Error](refused.Err); ok && apiErr.Code == basecamp.CodeNotFound {
			message += fmt.Sprintf(". Basecamp answers not found when the destination is not a %s this recording can go into, or is the one it is in now; a folder goes in folder_id", containerNouns[container])
		}
		return &filingError{Type: "filing_refused", Message: message, RetrySafe: true}
	}
	if started, ok := errors.AsType[*basecamp.FilingMayHaveStartedError](err); ok {
		if req.kind == basecamp.FilingCopy {
			return &filingError{
				Type:    "filing_may_have_started",
				Message: fmt.Sprintf("The request to copy it failed without Basecamp refusing it (%v). Basecamp may have made the copy, and copying again makes another: look for it in the destination first", started.Err),
			}
		}
		return &filingError{
			Type:      "filing_may_have_started",
			Message:   fmt.Sprintf("The request to move it failed without Basecamp refusing it (%v). Basecamp refuses a repeated move, so trying again either moves it or is refused because this one did", started.Err),
			RetrySafe: true,
		}
	}
	if failed, ok := errors.AsType[*basecamp.FilingFailedError](err); ok {
		return &filingError{
			Type:         "filing_failed",
			Message:      fmt.Sprintf("Basecamp started to %s it and reported filing %d failed. Read the recording before trying again", verb, failed.Filing.ID),
			FilingID:     failed.Filing.ID,
			FilingStatus: string(failed.Filing.Status),
		}
	}
	if unsettled, ok := errors.AsType[*basecamp.FilingUnsettledError](err); ok {
		return &filingError{
			Type:         "filing_unsettled",
			Message:      fmt.Sprintf("Basecamp had not finished filing %d when the wait ended (%v). Do not %s it again; check it with get_filing and filingId %d, which names the new recording once it lands", unsettled.Filing.ID, unsettled.Cause, verb, unsettled.Filing.ID),
			FilingID:     unsettled.Filing.ID,
			FilingStatus: string(unsettled.Filing.Status),
		}
	}
	return err
}

func pastTense(kind basecamp.FilingType) string {
	if kind == basecamp.FilingCopy {
		return "Copied"
	}
	return "Moved"
}

// withSourceID adds source_id, the recording a filing came from, to the
// recording it made.
func withSourceID(record json.RawMessage, sourceID int64) (json.RawMessage, error) {
	fields := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(record))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil, err
	}
	fields["source_id"] = sourceID
	return json.Marshal(fields)
}
