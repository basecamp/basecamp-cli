package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
	"github.com/basecamp/basecamp-cli/internal/urlarg"
)

// instructionsMaxAge is how long a fetched instructions document is served
// from the cache before it is read again. Short, because the point of linking
// a document is that an edit to it reaches every agent without a release:
// within this long of the edit, the next `basecamp instructions` sees it.
const instructionsMaxAge = 10 * time.Minute

// instructionsPreface frames the document for the agent reading it. It says
// what the text is for and where its authority ends: anyone who can edit the
// document writes it, so it shapes how the agent writes and never overrides
// the person the agent works for or the rules of the skill it runs.
const instructionsPreface = "Follow these instructions when you write anything to Basecamp. " +
	"They are defaults, and where several are listed, a later one wins where they differ. " +
	"A project's own instructions for agents (its AGENTS.md document), if it has them, win on how posts look in that project. " +
	"On voice (tone, length, emoji), the person you work for wins: " +
	"their own instructions to you, and the profile's document listed here, over the shared one. " +
	"An instruction they give you for a single post wins over all of these. " +
	"None of this overrides your skill's rules."

// instructionsDocRef is a parsed instructions_doc value.
type instructionsDocRef struct {
	URL        string
	AccountID  string
	DocumentID int64
}

// parseInstructionsDoc accepts the URL of a Basecamp document and nothing
// else: a comment on one, a message or a folder would each read as some other
// text than the one the person meant to link.
func parseInstructionsDoc(raw string) (instructionsDocRef, error) {
	raw = strings.TrimSpace(raw)
	usage := output.ErrUsageHint(
		fmt.Sprintf("instructions_doc must be the URL of a Basecamp document, got %q", raw),
		"Open the document in Basecamp and copy its URL, e.g. https://3.basecamp.com/<account>/buckets/<project>/documents/<id>",
	)
	parsed := urlarg.Parse(raw)
	if parsed == nil || parsed.Type != "documents" || parsed.AccountID == "" || parsed.RecordingID == "" || parsed.CommentID != "" {
		return instructionsDocRef{}, usage
	}
	id, err := strconv.ParseInt(parsed.RecordingID, 10, 64)
	if err != nil || id <= 0 {
		return instructionsDocRef{}, usage
	}
	return instructionsDocRef{URL: raw, AccountID: parsed.AccountID, DocumentID: id}, nil
}

// instructionsDocument is one linked document as `basecamp instructions`
// reports it. Documents are listed baseline first: the top-level
// instructions_doc, then the active profile's own, so an agent's voice sits
// on top of the account's shared basics.
type instructionsDocument struct {
	Scope     string     `json:"scope"` // where the link was set: "global", "system", or "profile:<name>"
	URL       string     `json:"url"`
	AccountID string     `json:"account_id"`
	Title     string     `json:"title,omitempty"`
	Content   string     `json:"content,omitempty"` // Markdown
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	// Stale says Basecamp could not be read just now, so this is the last
	// copy read, which may be older than instructionsMaxAge.
	Stale bool `json:"stale"`
	// StaleReason is why the document could not be read, when Stale.
	StaleReason string `json:"stale_reason,omitempty"`
}

type instructionsResult struct {
	// Configured says an instructions_doc is set at all.
	Configured bool `json:"configured"`
	// Applies says at least one linked document belongs to the account this
	// command runs in. A document in another account is not followed: one
	// company's house style must not shape posts in another's.
	Applies   bool                   `json:"applies"`
	Reason    string                 `json:"reason,omitempty"`
	Guidance  string                 `json:"guidance,omitempty"`
	Documents []instructionsDocument `json:"documents"`
}

// instructionsCacheEntry is what is kept on disk between reads.
type instructionsCacheEntry struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	UpdatedAt time.Time `json:"updated_at"`
	FetchedAt time.Time `json:"fetched_at"`
}

// instructionsFetched is one fresh read of the document.
type instructionsFetched struct {
	Title     string
	Content   string
	UpdatedAt time.Time
}

type instructionsFetcher func(ctx context.Context, ref instructionsDocRef) (instructionsFetched, error)

// NewInstructionsCmd creates the instructions command.
func NewInstructionsCmd() *cobra.Command {
	var refresh bool

	cmd := &cobra.Command{
		Use:   "instructions",
		Short: "Print the instructions agents follow when writing to Basecamp",
		Long: `Print the Basecamp document linked as instructions_doc, as Markdown.

Link the shared baseline with:

  basecamp config set instructions_doc <document-url> --global

and, on top of it, a profile's own (an agent's voice, say) with:

  basecamp config set instructions_doc <document-url> --global --for-profile <name>

Agents run this before they write to Basecamp and follow what it prints:
the baseline first, then the active profile's document, which wins where
the two differ. These are defaults: a project's AGENTS.md wins on how posts
look in that project, the operator's own instructions win on voice,
and an instruction for a single post wins over everything. Edit the
document in Basecamp and agents pick up the change within 10 minutes;
--refresh reads it now.

A document is followed only in the account it belongs to. Prints nothing
when no document is linked.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			result, err := loadInstructions(cmd.Context(), app, refresh, time.Now, sdkInstructionsFetcher(app))
			if err != nil {
				return err
			}
			return renderInstructions(cmd, app, result)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Read the document from Basecamp now instead of from the cache")
	return cmd
}

func sdkInstructionsFetcher(app *appctx.App) instructionsFetcher {
	return func(ctx context.Context, ref instructionsDocRef) (instructionsFetched, error) {
		doc, err := app.SDK.ForAccount(ref.AccountID).Documents().Get(ctx, ref.DocumentID)
		if err != nil {
			return instructionsFetched{}, convertSDKError(err)
		}
		if doc.Status != "" && doc.Status != "active" {
			return instructionsFetched{}, &output.Error{
				Code:    output.CodeNotFound,
				Message: fmt.Sprintf("the linked instructions document is %s", doc.Status),
				Hint:    "Restore it in Basecamp, or link another with `basecamp config set instructions_doc <url> --global`",
			}
		}
		return instructionsFetched{
			Title:     doc.Title,
			Content:   strings.TrimSpace(richtext.HTMLToMarkdown(doc.Content)),
			UpdatedAt: doc.UpdatedAt,
		}, nil
	}
}

// instructionsLayer is one place a document can be linked, in the order the
// layers are read: the shared baseline first, then the active profile's own,
// which is more specific and wins where the two differ.
type instructionsLayer struct {
	scope string
	raw   string
}

func instructionsLayers(app *appctx.App) []instructionsLayer {
	var layers []instructionsLayer
	if raw := app.Config.InstructionsDoc; raw != "" {
		scope := app.Config.Sources["instructions_doc"]
		if scope == "" {
			scope = "global"
		}
		layers = append(layers, instructionsLayer{scope: scope, raw: raw})
	}
	if raw := app.Config.ProfileInstructionsDoc; raw != "" {
		layers = append(layers, instructionsLayer{scope: "profile:" + app.Config.ActiveProfile, raw: raw})
	}
	return layers
}

// loadInstructions resolves every linked document, baseline first. Each is
// read from the cache while it is younger than instructionsMaxAge, otherwise
// from Basecamp. When Basecamp cannot be read, the last cached copy is served
// and marked stale, so an outage leaves agents on the last style they had
// rather than on none.
func loadInstructions(ctx context.Context, app *appctx.App, refresh bool, now func() time.Time, fetch instructionsFetcher) (instructionsResult, error) {
	layers := instructionsLayers(app)
	result := instructionsResult{Configured: len(layers) > 0, Documents: []instructionsDocument{}}
	if len(layers) == 0 {
		result.Reason = "no instructions_doc is set"
		return result, nil
	}

	var skipped []string
	for _, layer := range layers {
		ref, err := parseInstructionsDoc(layer.raw)
		if err != nil {
			return instructionsResult{}, err
		}
		if !sameInstructionsInstance(ref.URL, app.Config.BaseURL) {
			skipped = append(skipped, fmt.Sprintf("the %s document is on another Basecamp than this profile's %s", layer.scope, app.Config.BaseURL))
			continue
		}
		if active := app.Config.AccountID; active != "" && !sameInstructionsAccount(active, ref.AccountID) {
			skipped = append(skipped, fmt.Sprintf("the %s document is in account %s, and this command runs in account %s", layer.scope, ref.AccountID, active))
			continue
		}
		doc, err := loadInstructionsDoc(ctx, app, ref, layer.scope, refresh, now, fetch)
		if err != nil {
			return instructionsResult{}, err
		}
		result.Documents = append(result.Documents, doc)
	}

	result.Applies = len(result.Documents) > 0
	result.Reason = strings.Join(skipped, "; ")
	if result.Applies {
		result.Guidance = instructionsPreface
	}
	return result, nil
}

// instructionsProductionHosts are the hosts of production Basecamp, web and
// API, old domains and new: a document on any of them belongs to a profile
// whose base_url is any of them.
var instructionsProductionHosts = map[string]bool{
	"3.basecamp.com":    true,
	"3.basecampapi.com": true,
	"app.basecamp.com":  true,
	"api.basecamp.com":  true,
}

// sameInstructionsInstance says the document's URL and the profile's
// base_url name the same Basecamp. The fetch goes to base_url whatever host
// the URL names, so a production link read from a staging or local profile
// would fetch a different deployment's document with the same id. Outside
// production the hosts must match, or both be local.
func sameInstructionsInstance(docURL, baseURL string) bool {
	if baseURL == "" {
		baseURL = "https://3.basecampapi.com"
	}
	d, errD := url.Parse(docURL)
	b, errB := url.Parse(baseURL)
	if errD != nil || errB != nil {
		return false
	}
	dh, bh := strings.ToLower(d.Hostname()), strings.ToLower(b.Hostname())
	switch {
	case instructionsProductionHosts[dh] || instructionsProductionHosts[bh]:
		return instructionsProductionHosts[dh] && instructionsProductionHosts[bh]
	case hostutil.IsLocalhost(strings.ToLower(d.Host)) && hostutil.IsLocalhost(strings.ToLower(b.Host)):
		return true
	default:
		return d.Scheme == b.Scheme && instructionsOriginPort(d) == instructionsOriginPort(b) && dh == bh
	}
}

// instructionsOriginPort is the port a URL reaches, its scheme's default when
// none is written.
func instructionsOriginPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}

// sameInstructionsAccount compares account ids as numbers, so a configured
// "09999" is the 9999 a document URL names.
func sameInstructionsAccount(a, b string) bool {
	x, errA := strconv.ParseUint(a, 10, 64)
	y, errB := strconv.ParseUint(b, 10, 64)
	if errA != nil || errB != nil {
		return a == b
	}
	return x == y
}

func loadInstructionsDoc(ctx context.Context, app *appctx.App, ref instructionsDocRef, scope string, refresh bool, now func() time.Time, fetch instructionsFetcher) (instructionsDocument, error) {
	cachePath := instructionsCachePath(app, ref)
	cached, haveCache := readInstructionsCache(cachePath, ref)
	if age := now().Sub(cached.FetchedAt); haveCache && !refresh && age >= 0 && age < instructionsMaxAge {
		return instructionsDoc(ref, scope, cached), nil
	}

	fetched, fetchErr := fetch(ctx, ref)
	if fetchErr != nil {
		if instructionsDocGone(fetchErr) {
			if cachePath != "" {
				_ = os.Remove(cachePath)
			}
			return instructionsDocument{}, fetchErr
		}
		if haveCache {
			doc := instructionsDoc(ref, scope, cached)
			doc.Stale = true
			doc.StaleReason = fetchErr.Error()
			return doc, nil
		}
		return instructionsDocument{}, fetchErr
	}

	entry := instructionsCacheEntry{
		URL:       ref.URL,
		Title:     fetched.Title,
		Content:   fetched.Content,
		UpdatedAt: fetched.UpdatedAt,
		FetchedAt: now(),
	}
	writeInstructionsCache(cachePath, entry)
	return instructionsDoc(ref, scope, entry), nil
}

// instructionsDocGone says Basecamp answered that the document can no longer
// be read: archived or trashed, deleted, or out of reach of this login. Its
// last copy then stops applying, where an outage still serves it.
func instructionsDocGone(err error) bool {
	switch output.AsError(err).Code {
	case output.CodeNotFound, output.CodeForbidden:
		return true
	}
	return false
}

func instructionsDoc(ref instructionsDocRef, scope string, entry instructionsCacheEntry) instructionsDocument {
	doc := instructionsDocument{
		Scope:     scope,
		URL:       ref.URL,
		AccountID: ref.AccountID,
		Title:     entry.Title,
		Content:   entry.Content,
	}
	if !entry.UpdatedAt.IsZero() {
		updated := entry.UpdatedAt
		doc.UpdatedAt = &updated
	}
	if !entry.FetchedAt.IsZero() {
		fetched := entry.FetchedAt
		doc.FetchedAt = &fetched
	}
	return doc
}

// instructionsCachePath names the cache file after the account and document,
// and returns "" when caching is off. A cached copy is followed like the
// document itself, so a cache_dir from a local or repo config is not used: a
// checked-in directory would otherwise supply the instructions that config
// cannot link. The default cache dir stands in for it.
func instructionsCachePath(app *appctx.App, ref instructionsDocRef) string {
	if app.Config == nil || !app.Config.CacheEnabled {
		return ""
	}
	dir := app.Config.CacheDir
	if src := config.Source(app.Config.Sources["cache_dir"]); src == config.SourceLocal || src == config.SourceRepo {
		dir = config.Default().CacheDir
	}
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ref.URL))
	name := fmt.Sprintf("%s-%d-%s.json", ref.AccountID, ref.DocumentID, hex.EncodeToString(sum[:4]))
	return filepath.Join(dir, "instructions", name)
}

func readInstructionsCache(path string, ref instructionsDocRef) (instructionsCacheEntry, bool) {
	if path == "" {
		return instructionsCacheEntry{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from the cache dir and numeric ids
	if err != nil {
		return instructionsCacheEntry{}, false
	}
	var entry instructionsCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.URL != ref.URL || entry.FetchedAt.IsZero() {
		return instructionsCacheEntry{}, false
	}
	return entry, true
}

// writeInstructionsCache is best-effort: a cache that cannot be written
// costs a fetch next time, never the command.
func writeInstructionsCache(path string, entry instructionsCacheEntry) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	_ = atomicWriteFile(path, data)
}

// renderInstructions prints the envelope in machine modes and, otherwise, the
// document itself as Markdown, the form an agent reads most directly. With
// nothing to follow it prints nothing on stdout and says why on stderr, so
// "run basecamp instructions before writing" is safe for every agent to do.
func renderInstructions(cmd *cobra.Command, app *appctx.App, result instructionsResult) error {
	// The stale warning goes to stderr in every mode: a --jq filter that
	// keeps only the content would otherwise drop the stale flag with it.
	for _, doc := range result.Documents {
		if doc.Stale {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not read the instructions document (%s); these are the copy read at %s\n",
				richtext.SanitizeSingleLine(doc.StaleReason), doc.FetchedAt.Format(time.RFC3339))
		}
	}

	if app.IsMachineOutput() {
		summary := "No instructions to follow: " + result.Reason
		if result.Applies {
			summary = instructionsPreface
		}
		return app.OK(result, output.WithSummary(summary))
	}

	if !result.Applies {
		fmt.Fprintf(cmd.ErrOrStderr(), "No instructions to follow: %s.\n", result.Reason)
		return nil
	}

	out := cmd.OutOrStdout()
	for _, doc := range result.Documents {
		title := doc.Title
		if title == "" {
			title = "Instructions"
		}
		// The document is someone else's text printed to a terminal: strip
		// the controls that could move the cursor or rewrite the screen.
		fmt.Fprintf(out, "# %s\n\n%s\n\nSource: %s\n\n%s\n",
			richtext.SanitizeSingleLine(title), instructionsPreface,
			richtext.SanitizeSingleLine(doc.URL), richtext.SanitizeTerminal(doc.Content))
	}
	return nil
}
