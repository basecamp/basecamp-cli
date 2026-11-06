package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/config"
	"github.com/basecamp/basecamp-cli/internal/hostutil"
	"github.com/basecamp/basecamp-cli/internal/output"
	"github.com/basecamp/basecamp-cli/internal/richtext"
)

// instructionsMaxBytes caps one instructions source. The text is read into an
// agent's context before every write to Basecamp, so this size is already a
// long style guide; anything past it is a mistake (the wrong path or URL, a
// generated file), and reading it would push the agent's own rules out.
const instructionsMaxBytes = 64 * 1024

// instructionsMaxAge is how long a copy fetched from a URL is served from the
// cache before it is fetched again. Short, so a merged change reaches every
// agent within minutes, with no release and no pull.
const instructionsMaxAge = 10 * time.Minute

// instructionsFetchTimeout bounds one fetch, gh or HTTPS. An agent runs the
// command before every write, so a hung network must not hold it up for long:
// past this, the cached copy is served.
var instructionsFetchTimeout = 15 * time.Second

// instructionsPreface frames the text for the agent reading it. It comes from
// a source only the person's own global config can name, never from
// Basecamp, so it is the operator's direction on how to write, unlike
// anything the agent reads in Basecamp. It still never overrides the
// operator's own words or the skill's rules.
const instructionsPreface = "Follow these instructions when you write anything to Basecamp. " +
	"Your operator chose where they come from, and it is never Basecamp. " +
	"They are defaults, and where several are listed, a later one wins where they differ. " +
	"A project's own instructions for agents (its AGENTS.md document), if it has them, win on how posts look in that project. " +
	"On voice (tone, length, emoji), the person you work for wins: " +
	"their own instructions to you, and the profile's instructions listed here, over the shared one. " +
	"An instruction they give you for a single post wins over all of these. " +
	"None of this overrides your skill's rules."

// Instruction source kinds.
const (
	instructionsKindFile   = "file"
	instructionsKindGitHub = "github"
	instructionsKindURL    = "url"
)

// instructionsSource is a parsed instructions value: a local file, a file in
// a GitHub repository, or any other https URL.
type instructionsSource struct {
	Raw  string
	Kind string
	// Path is the absolute path of a local file.
	Path string
	// URL is what an https source fetches. For GitHub it is the
	// raw.githubusercontent.com address, the fallback when gh can't be used.
	URL string
	// Owner, Repo, Ref and File name a GitHub file.
	Owner, Repo, Ref, File string
}

func instructionsUsage(msg string) error {
	return output.ErrUsageHint(msg,
		"Give the path of a Markdown file on this computer, or its https URL, "+
			"e.g. https://github.com/<org>/<repo>/blob/main/docs/style.md")
}

// parseInstructionsSource reads a configured value. A URL is https (plain
// http only to localhost); a github.com URL must name one file, as its blob
// page does: https://github.com/<owner>/<repo>/blob/<ref>/<path>. The ref is
// the one segment after blob, so a branch whose name holds a slash is named
// by its commit or linked from raw.githubusercontent.com instead. Anything
// else is a local path: ~ is expanded, and the result must be absolute, so
// it never follows the working directory and the repo an agent stands in.
func parseInstructionsSource(raw string) (instructionsSource, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return instructionsSource{}, instructionsUsage("instructions needs a path or a URL")
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://") {
		return parseInstructionsURL(value)
	}
	if strings.Contains(value, "://") {
		return instructionsSource{}, instructionsUsage(fmt.Sprintf("instructions URL %q must be https", value))
	}
	if !filepath.IsAbs(value) {
		return instructionsSource{}, instructionsUsage(fmt.Sprintf("instructions %q is not an absolute path or an https URL", value))
	}
	return instructionsSource{Raw: value, Kind: instructionsKindFile, Path: filepath.Clean(value)}, nil
}

// checkInstructionsURL holds for the configured URL and for every redirect
// from it, so a fetch can't end up reading its instructions over cleartext.
func checkInstructionsURL(u *url.URL) error {
	if u.User != nil {
		return errors.New("instructions URL must not carry credentials")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "https" || (scheme == "http" && hostutil.IsLocalhost(strings.ToLower(u.Host))) {
		return nil
	}
	return fmt.Errorf("instructions URL %q must be https", u.Redacted())
}

func parseInstructionsURL(value string) (instructionsSource, error) {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return instructionsSource{}, instructionsUsage(fmt.Sprintf("instructions URL %q can't be read", value))
	}
	if err := checkInstructionsURL(u); err != nil {
		return instructionsSource{}, instructionsUsage(err.Error())
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return instructionsSource{Raw: value, Kind: instructionsKindURL, URL: u.String()}, nil
	}

	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) < 5 || parts[2] != "blob" || u.RawQuery != "" {
		return instructionsSource{}, output.ErrUsageHint(
			fmt.Sprintf("GitHub URL %q does not name a file", value),
			"Open the file on GitHub and copy its address: https://github.com/<org>/<repo>/blob/<branch>/<path>")
	}
	src := instructionsSource{Raw: value, Kind: instructionsKindGitHub}
	var files []string
	for i, p := range parts {
		seg, err := url.PathUnescape(p)
		if err != nil || seg == "" || seg == "." || seg == ".." {
			return instructionsSource{}, instructionsUsage(fmt.Sprintf("GitHub URL %q has an unusable path", value))
		}
		switch i {
		case 0:
			src.Owner = seg
		case 1:
			src.Repo = seg
		case 2:
		case 3:
			src.Ref = seg
		default:
			files = append(files, seg)
		}
	}
	src.File = strings.Join(files, "/")
	src.URL = instructionsRawGitHubBase + "/" + strings.Join(append([]string{parts[0], parts[1], parts[3]}, parts[4:]...), "/")
	return src, nil
}

// instructionsRawGitHubBase is where a GitHub file is fetched over plain
// HTTPS when gh can't fetch it. A variable so tests can point it elsewhere.
var instructionsRawGitHubBase = "https://raw.githubusercontent.com"

// resolveInstructionsSource turns the value given to `config set` into the
// one that is saved: a local path made absolute (expanding ~) and checked to
// name a readable file now, so a typo fails at set time; a URL checked for
// its shape. A URL is not fetched here: `basecamp instructions --refresh`
// does that.
func resolveInstructionsSource(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	lower := strings.ToLower(value)
	if strings.Contains(lower, "://") {
		src, err := parseInstructionsSource(value)
		if err != nil {
			return "", err
		}
		return src.Raw, nil
	}
	if value == "" {
		return "", instructionsUsage("instructions needs a path or a URL")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", instructionsUsage(fmt.Sprintf("cannot expand ~ in %q: %v", value, err))
		}
		value = filepath.Join(home, strings.TrimPrefix(value, "~"))
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", instructionsUsage(fmt.Sprintf("cannot resolve %q: %v", raw, err))
	}
	if _, err := readInstructionsFile(abs); err != nil {
		return "", err
	}
	return abs, nil
}

// instructionsDocument is one instructions source as `basecamp instructions`
// reports it. Sources are listed baseline first: the top-level instructions,
// then the active profile's own, so an agent's voice sits on top of the
// shared basics.
type instructionsDocument struct {
	Scope      string     `json:"scope"` // where it was set: "global", "system", or "profile:<name>"
	Source     string     `json:"source"`
	Kind       string     `json:"kind"`                  // file, github or url
	Content    string     `json:"content"`               // Markdown, as the source holds it
	Via        string     `json:"via,omitempty"`         // how a URL was read: gh, https, or cache
	ModifiedAt *time.Time `json:"modified_at,omitempty"` // a file's
	FetchedAt  *time.Time `json:"fetched_at,omitempty"`  // a URL's copy
	// Stale says the URL could not be fetched just now, so this is the last
	// copy fetched, which may be older than instructionsMaxAge.
	Stale bool `json:"stale"`
	// StaleReason is why the URL could not be fetched, when Stale.
	StaleReason string `json:"stale_reason,omitempty"`
}

type instructionsResult struct {
	// Configured says instructions are set at all.
	Configured bool                   `json:"configured"`
	Guidance   string                 `json:"guidance,omitempty"`
	Documents  []instructionsDocument `json:"documents"`
}

// instructionsFetched is one fresh read of a URL source.
type instructionsFetched struct {
	Content []byte
	Via     string
}

type instructionsFetcher func(ctx context.Context, src instructionsSource) (instructionsFetched, error)

// NewInstructionsCmd creates the instructions command.
func NewInstructionsCmd() *cobra.Command {
	var refresh bool

	cmd := &cobra.Command{
		Use:   "instructions",
		Short: "Print the instructions agents follow when writing to Basecamp",
		Long: `Print the instructions set with the instructions key, as Markdown.

Set the shared baseline, a local file or an https URL, with:

  basecamp config set instructions <path-or-url> --global

and, on top of it, a profile's own (an agent's voice, say) with:

  basecamp config set instructions <path-or-url> --global --for-profile <name>

Agents run this before they write to Basecamp and follow what it prints:
the baseline first, then the active profile's, which wins where the two
differ. These are defaults: a project's AGENTS.md wins on how posts look
in that project, the operator's own instructions win on voice, and an
instruction for a single post wins over everything.

A local file is read on every run. A URL is fetched and cached for 10
minutes; --refresh fetches it now. A GitHub file
(https://github.com/<org>/<repo>/blob/<ref>/<path>) is fetched with gh
when gh is installed and signed in, so private repositories work, and
over plain HTTPS otherwise. When a fetch fails, the last copy fetched is
served, marked stale, with a warning; with no copy at all, the command
fails.

Whoever can change the file or what the URL serves steers every agent on
this computer, so point it at something whose changes are reviewed, like
a file in a git repository. Only the global or system config can set it,
never a repo's, and it is never read from Basecamp.

Prints nothing when no instructions are set.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			app := appctx.FromContext(cmd.Context())
			result, err := loadInstructions(cmd.Context(), app, refresh, time.Now, fetchInstructions)
			if err != nil {
				return err
			}
			return renderInstructions(cmd, app, result)
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "Fetch a URL source now instead of from the cache")
	return cmd
}

// instructionsLayer is one place instructions can be set, in the order the
// layers are read: the shared baseline first, then the active profile's own,
// which is more specific and wins where the two differ.
type instructionsLayer struct {
	scope string
	raw   string
}

func instructionsLayers(app *appctx.App) []instructionsLayer {
	var layers []instructionsLayer
	if raw := app.Config.Instructions; raw != "" {
		scope := app.Config.Sources["instructions"]
		if scope == "" {
			scope = "global"
		}
		layers = append(layers, instructionsLayer{scope: scope, raw: raw})
	}
	if raw := app.Config.ProfileInstructions; raw != "" {
		layers = append(layers, instructionsLayer{scope: "profile:" + app.Config.ActiveProfile, raw: raw})
	}
	return layers
}

// loadInstructions reads every configured source, baseline first. A source
// that is set but can't be read, with no copy to fall back on, is an error,
// not silence: the person chose it, and an agent writing without it should
// say so rather than carry on as if nothing were set.
func loadInstructions(ctx context.Context, app *appctx.App, refresh bool, now func() time.Time, fetch instructionsFetcher) (instructionsResult, error) {
	layers := instructionsLayers(app)
	result := instructionsResult{Configured: len(layers) > 0, Documents: []instructionsDocument{}}
	for _, layer := range layers {
		src, err := parseInstructionsSource(layer.raw)
		if err != nil {
			return instructionsResult{}, err
		}
		var doc instructionsDocument
		if src.Kind == instructionsKindFile {
			doc, err = readInstructionsFile(src.Path)
		} else {
			doc, err = loadInstructionsURL(ctx, app, src, refresh, now, fetch)
		}
		if err != nil {
			return instructionsResult{}, err
		}
		doc.Scope = layer.scope
		doc.Source = src.Raw
		doc.Kind = src.Kind
		result.Documents = append(result.Documents, doc)
	}
	if result.Configured {
		result.Guidance = instructionsPreface
	}
	return result, nil
}

// readInstructionsFile reads one local file, refusing what can't be an
// instructions file: a relative path, a directory, a file over
// instructionsMaxBytes, or one that isn't UTF-8 text.
func readInstructionsFile(path string) (instructionsDocument, error) {
	fix := "Point it elsewhere with `basecamp config set instructions <path-or-url> --global`, or unset it with `basecamp config unset instructions --global`"
	if !filepath.IsAbs(path) {
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("instructions %q is not an absolute path", path), fix)
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return instructionsDocument{}, &output.Error{
				Code:    output.CodeNotFound,
				Message: fmt.Sprintf("the instructions file %s does not exist", path),
				Hint:    fix,
			}
		}
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("cannot read the instructions file %s: %v", path, err), fix)
	}
	if !info.Mode().IsRegular() {
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("the instructions file %s is not a regular file", path), fix)
	}
	data, err := readInstructionsBounded(path)
	if err != nil {
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("cannot read the instructions file %s: %v", path, err), fix)
	}
	content, err := checkInstructionsContent(data)
	if err != nil {
		return instructionsDocument{}, output.ErrUsageHint(fmt.Sprintf("the instructions file %s %v", path, err), fix)
	}
	modified := info.ModTime().UTC()
	return instructionsDocument{Content: content, ModifiedAt: &modified}, nil
}

// readInstructionsBounded reads one byte past the limit, enough for
// checkInstructionsContent to refuse a file without loading all of it.
func readInstructionsBounded(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the path comes from the person's own global config
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, instructionsMaxBytes+1))
}

// checkInstructionsContent applies the same limits to a file and a fetch.
func checkInstructionsContent(data []byte) (string, error) {
	if len(data) > instructionsMaxBytes {
		return "", fmt.Errorf("is over the %d-byte limit", instructionsMaxBytes)
	}
	if !utf8.Valid(data) {
		return "", errors.New("is not UTF-8 text")
	}
	// Trim blank lines and trailing space, never leading indentation: an
	// indented first line is a Markdown code block.
	content := strings.TrimRight(strings.TrimLeft(string(data), "\r\n"), " \t\r\n")
	if strings.TrimSpace(content) == "" {
		return "", errors.New("is empty")
	}
	return content, nil
}

// instructionsCacheEntry is what is kept on disk between fetches.
type instructionsCacheEntry struct {
	Source    string    `json:"source"`
	Content   string    `json:"content"`
	FetchedAt time.Time `json:"fetched_at"`
}

// loadInstructionsURL serves a URL source from the cache while its copy is
// younger than instructionsMaxAge, and otherwise fetches it. When the fetch
// fails, for any reason, the last copy is served and marked stale: it came
// from the same source the person chose, so an outage, a gh sign-out or a
// moved file leaves agents on the last style they had rather than on none.
// With no copy, the error is returned, so an agent never writes as if no
// instructions were set when some are.
func loadInstructionsURL(ctx context.Context, app *appctx.App, src instructionsSource, refresh bool, now func() time.Time, fetch instructionsFetcher) (instructionsDocument, error) {
	cachePath := instructionsCachePath(app, src)
	cached, haveCache := readInstructionsCache(cachePath, src)
	if age := now().Sub(cached.FetchedAt); haveCache && !refresh && age >= 0 && age < instructionsMaxAge {
		return instructionsURLDoc(cached, "cache"), nil
	}

	fetched, err := fetch(ctx, src)
	var content string
	if err == nil {
		content, err = checkInstructionsContent(fetched.Content)
		if err != nil {
			err = fmt.Errorf("what %s serves %w", src.Raw, err)
		}
	}
	if err != nil {
		if haveCache {
			doc := instructionsURLDoc(cached, "cache")
			doc.Stale = true
			doc.StaleReason = err.Error()
			return doc, nil
		}
		return instructionsDocument{}, &output.Error{
			Code:    output.CodeNetwork,
			Message: fmt.Sprintf("cannot fetch the instructions at %s, and no copy was fetched before: %v", src.Raw, err),
			Hint:    "Check the URL and your connection (for a private GitHub repository, `gh auth status`), then run `basecamp instructions --refresh`",
		}
	}

	entry := instructionsCacheEntry{Source: src.Raw, Content: content, FetchedAt: now()}
	writeInstructionsCache(cachePath, entry)
	return instructionsURLDoc(entry, fetched.Via), nil
}

func instructionsURLDoc(entry instructionsCacheEntry, via string) instructionsDocument {
	fetched := entry.FetchedAt
	return instructionsDocument{Content: entry.Content, Via: via, FetchedAt: &fetched}
}

// fetchInstructions fetches a URL source. A GitHub file goes through gh
// first, so a private repository the person can read works with no token
// handling here; when gh is missing or fails, it falls back to plain HTTPS,
// which reaches public repositories.
func fetchInstructions(ctx context.Context, src instructionsSource) (instructionsFetched, error) {
	if src.Kind != instructionsKindGitHub {
		return fetchInstructionsHTTPS(ctx, src.URL)
	}
	data, ghErr := fetchInstructionsGH(ctx, src)
	if ghErr == nil {
		return instructionsFetched{Content: data, Via: "gh"}, nil
	}
	fetched, httpErr := fetchInstructionsHTTPS(ctx, src.URL)
	if httpErr != nil {
		return instructionsFetched{}, fmt.Errorf("gh: %w; https: %w", ghErr, httpErr)
	}
	return fetched, nil
}

// fetchInstructionsGH gives gh its own deadline, so a slow failure leaves the
// HTTPS fallback its full time.
func fetchInstructionsGH(ctx context.Context, src instructionsSource) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, instructionsFetchTimeout)
	defer cancel()
	return instructionsGH(ctx, src)
}

// instructionsGH reads a GitHub file through `gh api`, as the person signed
// in to gh, always on github.com: GH_HOST would otherwise send the request to
// another server, and its answer would be cached as this source. It stops
// reading one byte past the limit, which checkInstructionsContent refuses.
// A variable so tests can stand in for gh.
var instructionsGH = func(ctx context.Context, src instructionsSource) ([]byte, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return nil, errors.New("gh is not installed")
	}
	segs := strings.Split(src.File, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	endpoint := fmt.Sprintf("repos/%s/%s/contents/%s?ref=%s",
		url.PathEscape(src.Owner), url.PathEscape(src.Repo), strings.Join(segs, "/"), url.QueryEscape(src.Ref))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, "api", "--hostname", "github.com", "-H", "Accept: application/vnd.github.raw+json", endpoint) //nolint:gosec // G204: fixed arguments; the endpoint is built from escaped segments
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.WaitDelay = time.Second // a child of gh holding stderr must not hold this up
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, instructionsMaxBytes+1))
	if len(out) > instructionsMaxBytes {
		cancel()
		_ = stdout.Close()
		_ = cmd.Wait()
		return out, nil
	}
	if err := cmd.Wait(); err != nil || readErr != nil {
		if err == nil {
			err = readErr
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(richtext.SanitizeSingleLine(msg))
	}
	return out, nil
}

// instructionsHTTPClient fetches https sources. A variable so tests can
// reach a local server.
var instructionsHTTPClient = &http.Client{Timeout: instructionsFetchTimeout, CheckRedirect: checkInstructionsRedirect}

func checkInstructionsRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	return checkInstructionsURL(req.URL)
}

func fetchInstructionsHTTPS(ctx context.Context, rawURL string) (instructionsFetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return instructionsFetched{}, err
	}
	resp, err := instructionsHTTPClient.Do(req)
	if err != nil {
		return instructionsFetched{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return instructionsFetched{}, fmt.Errorf("%s answered %s", rawURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, instructionsMaxBytes+1))
	if err != nil {
		return instructionsFetched{}, err
	}
	return instructionsFetched{Content: data, Via: "https"}, nil
}

// instructionsCachePath names the cache file after the source, and returns ""
// when caching is off. A cached copy is followed like the source itself, so
// a cache_dir from a local or repo config is not used: a checked-in directory
// would otherwise supply the instructions that config cannot set. The
// default cache dir stands in for it.
func instructionsCachePath(app *appctx.App, src instructionsSource) string {
	if app.Config == nil || !app.Config.CacheEnabled {
		return ""
	}
	dir := app.Config.CacheDir
	if s := config.Source(app.Config.Sources["cache_dir"]); s == config.SourceLocal || s == config.SourceRepo {
		dir = config.Default().CacheDir
	}
	if dir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(src.Raw))
	return filepath.Join(dir, "instructions", hex.EncodeToString(sum[:8])+".json")
}

func readInstructionsCache(path string, src instructionsSource) (instructionsCacheEntry, bool) {
	if path == "" {
		return instructionsCacheEntry{}, false
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is built from the cache dir and a hash
	if err != nil {
		return instructionsCacheEntry{}, false
	}
	var entry instructionsCacheEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.Source != src.Raw || entry.FetchedAt.IsZero() {
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
// text itself as Markdown, the form an agent reads most directly. With
// nothing set it prints nothing on stdout and says so on stderr, so "run
// basecamp instructions before writing" is safe for every agent to do.
func renderInstructions(cmd *cobra.Command, app *appctx.App, result instructionsResult) error {
	// The stale warning goes to stderr in every mode: a --jq filter that
	// keeps only the content would otherwise drop the stale flag with it.
	for _, doc := range result.Documents {
		if doc.Stale {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not fetch the instructions at %s (%s); these are the copy fetched at %s\n",
				richtext.SanitizeSingleLine(doc.Source), richtext.SanitizeSingleLine(doc.StaleReason), doc.FetchedAt.Format(time.RFC3339))
		}
	}

	if app.IsMachineOutput() {
		summary := "No instructions to follow: none are set"
		if result.Configured {
			summary = instructionsPreface
		}
		return app.OK(result, output.WithSummary(summary))
	}

	if !result.Configured {
		fmt.Fprintln(cmd.ErrOrStderr(), "No instructions to follow: none are set.")
		return nil
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s\n", instructionsPreface)
	for _, doc := range result.Documents {
		// Printed to a terminal: strip the controls that could move the
		// cursor or rewrite the screen.
		fmt.Fprintf(out, "\n---\n\nSource: %s (%s)\n\n%s\n",
			richtext.SanitizeSingleLine(doc.Source), richtext.SanitizeSingleLine(doc.Scope), richtext.SanitizeTerminal(doc.Content))
	}
	return nil
}
