package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// Cloud files and Google documents are links to outside files that sit in
// Docs & Files beside documents and uploads. files show reads them; the
// commands here add and change them.
//
// Basecamp replaces the whole record on update (CloudFilesController#update
// and GoogleDocumentsController#update build a new recordable from the
// permitted params alone), so a field the request leaves out is erased. The
// update commands read the record first and send back every field no flag
// changes, so only what was asked for changes.

// googleDocumentTypes are the document types Basecamp accepts.
var googleDocumentTypes = []string{"doc", "sheet", "slide", "other"}

// inferGoogleDocumentType reads the document type from a docs.google.com
// link's path, and answers "" for anything else.
func inferGoogleDocumentType(raw string) string {
	switch inferDoorService(raw) {
	case "google_doc":
		return "doc"
	case "google_sheet":
		return "sheet"
	case "google_slide":
		return "slide"
	}
	return ""
}

// linkFolder resolves where a new cloud file or Google document goes: the
// project, and the folder (the project's Docs & Files root unless
// --vault/--folder names one).
func linkFolder(cmd *cobra.Command, app *appctx.App, project, vaultID string) (string, int64, int64, error) {
	projectID := project
	if projectID == "" {
		projectID = app.Flags.Project
	}
	if projectID == "" {
		projectID = app.Config.ProjectID
	}
	if projectID == "" {
		if err := ensureProject(cmd, app); err != nil {
			return "", 0, 0, err
		}
		projectID = app.Config.ProjectID
	}

	resolvedProjectID, _, err := app.Names.ResolveProject(cmd.Context(), projectID)
	if err != nil {
		return "", 0, 0, err
	}
	bucketID, err := strconv.ParseInt(resolvedProjectID, 10, 64)
	if err != nil {
		return "", 0, 0, output.ErrUsage("Invalid project ID")
	}

	resolvedVaultID := vaultID
	if resolvedVaultID == "" {
		resolvedVaultID, err = getVaultID(cmd, app, resolvedProjectID)
		if err != nil {
			return "", 0, 0, err
		}
	}
	vaultIDNum, err := strconv.ParseInt(resolvedVaultID, 10, 64)
	if err != nil {
		return "", 0, 0, output.ErrUsage("Invalid folder ID")
	}

	return resolvedProjectID, bucketID, vaultIDNum, nil
}

// linkDescription converts a --description value (Markdown, or - for stdin)
// to the rich text Basecamp stores, uploading any local images it names. An
// empty value stays empty.
func linkDescription(cmd *cobra.Command, app *appctx.App, value string) (string, error) {
	desc, err := resolveContentValue(cmd, value, -1, "--description")
	if err != nil {
		return "", err
	}
	if desc == "" {
		return "", nil
	}
	return resolveLocalImages(cmd, app, richTextToHTML(cmd, desc))
}

// linkIDArg reads a cloud file's or Google document's ID, or the ID in its
// Basecamp URL, as doorIDArg does: a URL must be on a trusted Basecamp host,
// name one record of urlType, and name the configured account, whose place
// it takes when none is configured.
func linkIDArg(app *appctx.App, arg, urlType, what, hint string) (int64, error) {
	idStr := arg
	if urlarg.IsURL(arg) {
		if !hostutil.IsTrustedBasecampHost(arg, app.Config.BaseURL) {
			return 0, output.ErrUsage("refusing untrusted host in URL — expected a Basecamp URL")
		}
		parsed := urlarg.Parse(arg)
		if parsed == nil || parsed.IsCollection || parsed.Type != urlType || parsed.RecordingID == "" {
			return 0, output.ErrUsageHint("The URL does not name one "+what, hint)
		}
		if parsed.AccountID != "" {
			switch {
			case app.Config.AccountID == "":
				app.Config.AccountID = parsed.AccountID
			case !accountIDsEqual(parsed.AccountID, app.Config.AccountID):
				return 0, output.ErrUsage(fmt.Sprintf("URL account %s does not match the configured account %s", parsed.AccountID, app.Config.AccountID))
			}
		}
		idStr = parsed.RecordingID
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, output.ErrUsage("Invalid " + what + " ID")
	}
	return id, nil
}

// linkUpdateFlagsGiven reports whether any of the named flags was given.
func linkUpdateFlagsGiven(cmd *cobra.Command, names ...string) bool {
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

// rejectEmptyLinkFlags refuses an empty --url, --title or --service/--type:
// Basecamp requires the url and the service or type, and an empty title would
// read back as "Untitled".
func rejectEmptyLinkFlags(cmd *cobra.Command, names ...string) error {
	for _, name := range names {
		if !cmd.Flags().Changed(name) {
			continue
		}
		value, err := cmd.Flags().GetString(name)
		if err == nil && strings.TrimSpace(value) == "" {
			return output.ErrUsage(fmt.Sprintf("--%s cannot be empty", name))
		}
	}
	return nil
}

func newCloudFilesCmd(project, vaultID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud-files",
		Short: "Add and change cloud files",
		Long: `Add and change cloud files: links to files on an outside service (Dropbox,
Box, Figma, OneDrive, ...) kept in a project's Docs & Files.

  basecamp files cloud-files create --url https://www.dropbox.com/s/abc/plan.pdf --in MyProject
  basecamp files cloud-files update 12345 --title "Plan (final)"

files show reads one, by the ID create prints or its Basecamp URL.`,
		Annotations: map[string]string{
			"agent_notes": "create needs --url and --in <project>; --service is inferred from the URL's host, other when unknown.\n" +
				"--subscribe/--no-subscribe on create choose who is subscribed and notified; omitted, Basecamp's default applies.\n" +
				"update reads the cloud file first and sends back every field no flag changes, because Basecamp replaces the whole record.",
		},
	}

	cmd.AddCommand(
		newCloudFilesCreateCmd(project, vaultID),
		newCloudFilesUpdateCmd(),
	)

	return cmd
}

func newCloudFilesCreateCmd(project, vaultID *string) *cobra.Command {
	var linkURL, title, service, description, subscribe string
	var noSubscribe, visibleToClients bool

	cmd := &cobra.Command{
		Use:   "create [title]",
		Short: "Add a cloud file to Docs & Files",
		Long: `Add a cloud file to a project's Docs & Files, or to a folder with --folder.

--url is required. --service is the service code (dropbox, box, figma,
one_drive, other, ...); without it the service is inferred from the URL's host,
and a host with no known service is added as other. Basecamp refuses a URL that
does not match the service.

--subscribe names who is subscribed and notified; --no-subscribe notifies
nobody. --description is Markdown, converted to rich text (- reads stdin).

  basecamp files cloud-files create --url https://www.dropbox.com/s/abc/plan.pdf --title "Plan" --in MyProject
  basecamp files cloud-files create "Mockups" --url https://www.figma.com/file/abc/Mockups --subscribe "Jane,Bob" --in MyProject`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && title == "" {
				title = args[0]
			}
			if linkURL == "" {
				return output.ErrUsageHint("--url is required",
					"basecamp files cloud-files create --url https://example.com/file --title \"Title\" --in <project>")
			}
			if service == "" {
				service = inferDoorService(linkURL)
				if service == "" {
					service = "other"
				}
			}
			if err := rejectSubscribeConflict(cmd.Flags().Changed("subscribe"), noSubscribe, subscribe); err != nil {
				return err
			}
			if err := requireNumericID(*vaultID, "folder ID"); err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			// Taken before the account is resolved or adopted from a URL, so
			// the follow-ups name the account this run used.
			scope := newFollowUpScope(app)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			subs, err := applySubscribeFlags(cmd.Context(), app.Names, subscribe, cmd.Flags().Changed("subscribe"), noSubscribe)
			if err != nil {
				return err
			}

			resolvedProjectID, bucketID, vaultIDNum, err := linkFolder(cmd, app, *project, *vaultID)
			if err != nil {
				return err
			}
			vis, err := resolveVaultClientVisibility(cmd, app, *vaultID, vaultIDNum, visibleToClients)
			if err != nil {
				return err
			}
			// Local images upload only once the folder is known to take them.
			desc, err := linkDescription(cmd, app, description)
			if err != nil {
				return err
			}

			cloudFile, err := app.Account().CloudFiles().Create(cmd.Context(), bucketID, vaultIDNum, &basecamp.CreateCloudFileRequest{
				URL:              linkURL,
				Service:          service,
				Title:            title,
				Description:      desc,
				Subscriptions:    subs,
				VisibleToClients: vis,
			})
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(cloudFile,
				output.WithSummary(fmt.Sprintf("Created cloud file #%d: %s → %s", cloudFile.ID, cloudFile.Title, cloudFile.URL)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp files show %d --type cloud_file --in %s%s", cloudFile.ID, resolvedProjectID, scope.args(app.Config.AccountID)),
						Description: "View cloud file",
					},
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp files cloud-files update %d --title \"New title\"%s", cloudFile.ID, scope.args(app.Config.AccountID)),
						Description: "Update cloud file",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&linkURL, "url", "", "Address of the file on the outside service (required)")
	cmd.Flags().StringVar(&title, "title", "", "Title (Basecamp uses \"Untitled\" without one)")
	cmd.Flags().StringVar(&service, "service", "", "Service code (dropbox, box, figma, other, ...); inferred from the URL when omitted")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Description (Markdown); use - to read from stdin")
	cmd.Flags().StringVar(&subscribe, "subscribe", "", "Subscribe and notify specific people (comma-separated names, emails, IDs, or \"me\")")
	cmd.Flags().BoolVar(&noSubscribe, "no-subscribe", false, "Don't subscribe anyone else (silent, no notifications)")
	cmd.Flags().BoolVar(&visibleToClients, "visible-to-clients", false, "Make the cloud file visible to clients (root Docs & Files folder only; a nested folder inherits its folder's visibility). Omit for the server default.")
	addRichTextFormatFlag(cmd)
	allowDash(cmd, "flag:description")
	registerDoorServiceCompletion(cmd)

	return cmd
}

func newCloudFilesUpdateCmd() *cobra.Command {
	var linkURL, title, service, description string

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a cloud file",
		Long: `Update a cloud file. Only the flags given change: the cloud file is read
first and everything else is sent back as it is, because Basecamp replaces the
whole record on update.

A new --url without --service changes the service too when the URL's host names
a known one (dropbox.com, figma.com, ...); otherwise the service stays as it is.
--description "" clears the description.

  basecamp files cloud-files update 12345 --title "Plan (final)"
  basecamp files cloud-files update 12345 --url https://www.dropbox.com/s/xyz/plan-v2.pdf`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !linkUpdateFlagsGiven(cmd, "url", "title", "service", "description") {
				return noChanges(cmd)
			}
			if err := rejectEmptyLinkFlags(cmd, "url", "title", "service"); err != nil {
				return err
			}
			app := appctx.FromContext(cmd.Context())
			// Taken before the account is resolved or adopted from a URL, so
			// the follow-ups name the account this run used.
			scope := newFollowUpScope(app)
			cloudFileID, err := linkIDArg(app, args[0], "cloud_files", "cloud file",
				"Pass the cloud file's ID, or its URL (…/buckets/<project>/cloud_files/<id>); create prints the ID")
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			current, err := app.Account().CloudFiles().Get(cmd.Context(), cloudFileID)
			if err != nil {
				return convertSDKError(err)
			}

			req := &basecamp.UpdateCloudFileRequest{
				URL:         current.URL,
				Service:     current.Service.Code,
				Title:       current.Title,
				Description: current.Description,
			}
			if cmd.Flags().Changed("url") {
				req.URL = linkURL
				if inferred := inferDoorService(linkURL); inferred != "" {
					req.Service = inferred
				}
			}
			if cmd.Flags().Changed("service") {
				req.Service = service
			}
			if cmd.Flags().Changed("title") {
				req.Title = title
			}
			if cmd.Flags().Changed("description") {
				if req.Description, err = linkDescription(cmd, app, description); err != nil {
					return err
				}
			}

			cloudFile, err := app.Account().CloudFiles().Update(cmd.Context(), current.ID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(cloudFile,
				output.WithSummary(fmt.Sprintf("Updated cloud file #%d: %s → %s", cloudFile.ID, cloudFile.Title, cloudFile.URL)),
				output.WithBreadcrumbs(output.Breadcrumb{
					Action:      "show",
					Cmd:         fmt.Sprintf("basecamp files show %d --type cloud_file%s", cloudFile.ID, scope.args(app.Config.AccountID)),
					Description: "View cloud file",
				}),
			)
		},
	}

	cmd.Flags().StringVar(&linkURL, "url", "", "New address")
	cmd.Flags().StringVar(&title, "title", "", "New title")
	cmd.Flags().StringVar(&service, "service", "", "New service code (dropbox, box, figma, other, ...)")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description (Markdown); \"\" clears it, - reads stdin")
	addRichTextFormatFlag(cmd)
	allowDash(cmd, "flag:description")
	registerDoorServiceCompletion(cmd)

	return cmd
}

func newGoogleDocsCmd(project, vaultID *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "google-docs",
		Short: "Add and change Google Docs",
		Long: `Add and change Google Docs, Sheets and Slides linked in a project's Docs & Files.

  basecamp files google-docs create --url https://docs.google.com/document/d/abc/edit --in MyProject
  basecamp files google-docs update 12345 --title "Spec (final)"

files show reads one, by the ID create prints or its Basecamp URL.`,
		Annotations: map[string]string{
			"agent_notes": "create needs --url and --in <project>; --type (doc, sheet, slide, other) is inferred from a docs.google.com URL.\n" +
				"create publishes unless --draft; --subscribe/--no-subscribe choose who is subscribed and notified.\n" +
				"update reads the document first and sends back every field no flag changes, because Basecamp replaces the whole record.\n" +
				"update --subscribe/--no-subscribe change a draft's subscribers only; --publish publishes a draft.",
		},
	}

	cmd.AddCommand(
		newGoogleDocsCreateCmd(project, vaultID),
		newGoogleDocsUpdateCmd(),
	)

	return cmd
}

// googleDocumentType settles a Google document's type: --type when given,
// else what the URL says, else fallback.
func googleDocumentType(cmd *cobra.Command, docType, linkURL, fallback string) (string, error) {
	if cmd.Flags().Changed("type") {
		docType = strings.ToLower(strings.TrimSpace(docType))
		for _, t := range googleDocumentTypes {
			if docType == t {
				return docType, nil
			}
		}
		return "", output.ErrUsageHint(fmt.Sprintf("Invalid --type: %s", docType), "Use: doc, sheet, slide, or other")
	}
	if inferred := inferGoogleDocumentType(linkURL); inferred != "" {
		return inferred, nil
	}
	return fallback, nil
}

func newGoogleDocsCreateCmd(project, vaultID *string) *cobra.Command {
	var linkURL, title, docType, description, subscribe string
	var draft, noSubscribe, visibleToClients bool

	cmd := &cobra.Command{
		Use:   "create [title]",
		Short: "Add a Google Doc to Docs & Files",
		Long: `Add a Google Doc, Sheet or Slides to a project's Docs & Files, or to a folder
with --folder.

--url is required. --type is doc, sheet, slide or other; a docs.google.com URL
says which, and anything else is other.

It is published unless --draft is passed. --subscribe names who is subscribed
and notified; --no-subscribe notifies nobody. --description is Markdown,
converted to rich text (- reads stdin).

  basecamp files google-docs create --url https://docs.google.com/document/d/abc/edit --title "Spec" --in MyProject
  basecamp files google-docs create "Budget" --url https://docs.google.com/spreadsheets/d/abc/edit --no-subscribe --in MyProject`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && title == "" {
				title = args[0]
			}
			if linkURL == "" {
				return output.ErrUsageHint("--url is required",
					"basecamp files google-docs create --url https://docs.google.com/document/d/<id>/edit --title \"Title\" --in <project>")
			}
			documentType, err := googleDocumentType(cmd, docType, linkURL, "other")
			if err != nil {
				return err
			}
			if err := rejectSubscribeConflict(cmd.Flags().Changed("subscribe"), noSubscribe, subscribe); err != nil {
				return err
			}
			if err := requireNumericID(*vaultID, "folder ID"); err != nil {
				return err
			}

			app := appctx.FromContext(cmd.Context())
			// Taken before the account is resolved or adopted from a URL, so
			// the follow-ups name the account this run used.
			scope := newFollowUpScope(app)
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			subs, err := applySubscribeFlags(cmd.Context(), app.Names, subscribe, cmd.Flags().Changed("subscribe"), noSubscribe)
			if err != nil {
				return err
			}

			resolvedProjectID, bucketID, vaultIDNum, err := linkFolder(cmd, app, *project, *vaultID)
			if err != nil {
				return err
			}
			vis, err := resolveVaultClientVisibility(cmd, app, *vaultID, vaultIDNum, visibleToClients)
			if err != nil {
				return err
			}
			// Local images upload only once the folder is known to take them.
			desc, err := linkDescription(cmd, app, description)
			if err != nil {
				return err
			}

			// Basecamp keeps a new Google document as a draft unless told
			// otherwise; documents create publishes by default, and so does this.
			status := "active"
			if draft {
				status = "drafted"
			}

			doc, err := app.Account().GoogleDocuments().Create(cmd.Context(), bucketID, vaultIDNum, &basecamp.CreateGoogleDocumentRequest{
				URL:              linkURL,
				DocumentType:     documentType,
				Title:            title,
				Description:      desc,
				Status:           status,
				Subscriptions:    subs,
				VisibleToClients: vis,
			})
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(doc,
				output.WithSummary(fmt.Sprintf("Created Google %s #%d: %s", doc.DocumentType, doc.ID, doc.Title)),
				output.WithBreadcrumbs(
					output.Breadcrumb{
						Action:      "show",
						Cmd:         fmt.Sprintf("basecamp files show %d --type google_document --in %s%s", doc.ID, resolvedProjectID, scope.args(app.Config.AccountID)),
						Description: "View Google document",
					},
					output.Breadcrumb{
						Action:      "update",
						Cmd:         fmt.Sprintf("basecamp files google-docs update %d --title \"New title\"%s", doc.ID, scope.args(app.Config.AccountID)),
						Description: "Update Google document",
					},
				),
			)
		},
	}

	cmd.Flags().StringVar(&linkURL, "url", "", "Google Docs, Sheets or Slides link (required)")
	cmd.Flags().StringVar(&title, "title", "", "Title (Basecamp uses \"Untitled\" without one)")
	cmd.Flags().StringVar(&docType, "type", "", "Document type: doc, sheet, slide or other; inferred from the URL when omitted")
	cmd.Flags().StringVarP(&description, "description", "d", "", "Description (Markdown); use - to read from stdin")
	cmd.Flags().BoolVar(&draft, "draft", false, "Create as a draft (default: published)")
	cmd.Flags().StringVar(&subscribe, "subscribe", "", "Subscribe and notify specific people (comma-separated names, emails, IDs, or \"me\")")
	cmd.Flags().BoolVar(&noSubscribe, "no-subscribe", false, "Don't subscribe anyone else (silent, no notifications)")
	cmd.Flags().BoolVar(&visibleToClients, "visible-to-clients", false, "Make the document visible to clients (root Docs & Files folder only; a nested folder inherits its folder's visibility). Omit for the server default.")
	addRichTextFormatFlag(cmd)
	allowDash(cmd, "flag:description")
	registerGoogleDocumentTypeCompletion(cmd)

	return cmd
}

func newGoogleDocsUpdateCmd() *cobra.Command {
	var linkURL, title, docType, description, subscribe string
	var publish, noSubscribe bool

	cmd := &cobra.Command{
		Use:   "update <id|url>",
		Short: "Update a Google Doc",
		Long: `Update a Google document. Only the flags given change: the document is read
first and everything else is sent back as it is, because Basecamp replaces the
whole record on update.

A new docs.google.com --url without --type changes the type to match it.
--description "" clears the description. --publish publishes a draft.

--subscribe and --no-subscribe change who a draft's subscribers are, and so who
is notified when it is published; Basecamp leaves a published document's
subscribers alone.

  basecamp files google-docs update 12345 --title "Spec (final)"
  basecamp files google-docs update 12345 --publish --subscribe "Jane,Bob"`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !linkUpdateFlagsGiven(cmd, "url", "title", "type", "description", "publish", "subscribe", "no-subscribe") {
				return noChanges(cmd)
			}
			if err := rejectEmptyLinkFlags(cmd, "url", "title", "type"); err != nil {
				return err
			}
			if cmd.Flags().Changed("publish") && !publish {
				return output.ErrUsage("--publish=false is not supported: a published document can't go back to a draft")
			}
			if err := rejectSubscribeConflict(cmd.Flags().Changed("subscribe"), noSubscribe, subscribe); err != nil {
				return err
			}
			app := appctx.FromContext(cmd.Context())
			// Taken before the account is resolved or adopted from a URL, so
			// the follow-ups name the account this run used.
			scope := newFollowUpScope(app)
			docID, err := linkIDArg(app, args[0], "google_documents", "Google document",
				"Pass the Google document's ID, or its URL (…/buckets/<project>/google_documents/<id>); create prints the ID")
			if err != nil {
				return err
			}
			if err := ensureAccount(cmd, app); err != nil {
				return err
			}

			subs, err := applySubscribeFlags(cmd.Context(), app.Names, subscribe, cmd.Flags().Changed("subscribe"), noSubscribe)
			if err != nil {
				return err
			}

			current, err := app.Account().GoogleDocuments().Get(cmd.Context(), docID)
			if err != nil {
				return convertSDKError(err)
			}

			// Status is left out unless --publish is given: Basecamp then
			// keeps the document's own status.
			req := &basecamp.UpdateGoogleDocumentRequest{
				URL:           current.URL,
				DocumentType:  current.DocumentType,
				Title:         current.Title,
				Description:   current.Description,
				Subscriptions: subs,
			}
			if cmd.Flags().Changed("url") {
				req.URL = linkURL
				if inferred := inferGoogleDocumentType(linkURL); inferred != "" {
					req.DocumentType = inferred
				}
			}
			if req.DocumentType, err = googleDocumentType(cmd, docType, "", req.DocumentType); err != nil {
				return err
			}
			if cmd.Flags().Changed("title") {
				req.Title = title
			}
			if cmd.Flags().Changed("description") {
				if req.Description, err = linkDescription(cmd, app, description); err != nil {
					return err
				}
			}
			if publish {
				req.Status = "active"
			}

			doc, err := app.Account().GoogleDocuments().Update(cmd.Context(), current.ID, req)
			if err != nil {
				return convertSDKError(err)
			}

			return app.OK(doc,
				output.WithSummary(fmt.Sprintf("Updated Google %s #%d: %s", doc.DocumentType, doc.ID, doc.Title)),
				output.WithBreadcrumbs(output.Breadcrumb{
					Action:      "show",
					Cmd:         fmt.Sprintf("basecamp files show %d --type google_document%s", doc.ID, scope.args(app.Config.AccountID)),
					Description: "View Google document",
				}),
			)
		},
	}

	cmd.Flags().StringVar(&linkURL, "url", "", "New Google Docs, Sheets or Slides link")
	cmd.Flags().StringVar(&title, "title", "", "New title")
	cmd.Flags().StringVar(&docType, "type", "", "New document type: doc, sheet, slide or other")
	cmd.Flags().StringVarP(&description, "description", "d", "", "New description (Markdown); \"\" clears it, - reads stdin")
	cmd.Flags().BoolVar(&publish, "publish", false, "Publish a draft")
	cmd.Flags().StringVar(&subscribe, "subscribe", "", "A draft's subscribers, notified when it is published (comma-separated names, emails, IDs, or \"me\")")
	cmd.Flags().BoolVar(&noSubscribe, "no-subscribe", false, "Subscribe nobody else to a draft")
	addRichTextFormatFlag(cmd)
	allowDash(cmd, "flag:description")
	registerGoogleDocumentTypeCompletion(cmd)

	return cmd
}

func registerGoogleDocumentTypeCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("type", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return googleDocumentTypes, cobra.ShellCompDirectiveNoFileComp
	})
}
