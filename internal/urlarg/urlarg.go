// Package urlarg provides utilities for parsing Basecamp URLs into IDs.
// This allows users to paste URLs from the browser as command arguments.
package urlarg

import (
	"regexp"
	"strings"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// chatAtRe matches the Basecamp web URL form chats/{chatID}@{lineID}
// and normalizes it to chats/{chatID}/lines/{lineID} before routing.
var chatAtRe = regexp.MustCompile(`(/chats/\d+)@(\d+)`)

// pingAtRe matches the Basecamp web URL form circles/{pingID}@{lineID}, a chat
// line in a Ping. A Ping is its own bucket, so the form is parsed as a line
// whose bucket is the Ping; the Campfire is not in the URL.
var pingAtRe = regexp.MustCompile(`/circles/(\d+)@(\d+)`)

// pingSidebarRe matches the sidebar form my/sidebar/circles/{pingID}, which
// Basecamp links to for the same Ping, and drops the my/sidebar prefix.
var pingSidebarRe = regexp.MustCompile(`/my/sidebar(/circles/\d+)`)

// slashRunRe matches a run of slashes, which Rails routes as one.
var slashRunRe = regexp.MustCompile(`/{2,}`)

// jsonSuffixRe matches the .json an API URL's path ends in.
var jsonSuffixRe = regexp.MustCompile(`^([^?#]*)\.json([?#]|$)`)

// itemPaths are paths whose segments past an item name another item, each
// rewritten to that item's own path as ${1}${2}: a child nested under its
// parent, which bc3 also serves at top level, and an upload's download and
// preview links. bc3 links those to a version even when it is the latest, so
// they name the upload; a download's filename, which may be all digits, goes
// with them.
var itemPaths = []*regexp.Regexp{
	regexp.MustCompile(`/vaults/\d+(/(?:vaults|documents|google_documents|cloud_files|uploads)/\d+)([/?#]|$)`),
	regexp.MustCompile(`/todolists/\d+(/todos/\d+)([/?#]|$)`),
	regexp.MustCompile(`/questionnaires/\d+(/questions/\d+)([/?#]|$)`),
	regexp.MustCompile(`(/uploads/\d+)(?:/versions/\d+)?/download/[^?#]+([?#]|$)`),
	regexp.MustCompile(`(/uploads/\d+)/versions/\d+/representations/[^/?#]+([?#]|$)`),
}

// structuralPath is the whole path of each shape the router's structural
// fallback recognizes. The fallback matches only a prefix of the path.
var structuralPath = regexp.MustCompile(`^/\d+(?:/projects/\d+|/buckets/\d+/(?:card_tables/[^/]+/\d+|[^/]+(?:/\d+)?))?$`)

// trailingWords matches the segments after a path's last ID when each starts
// with a letter: a page of that item, such as edit or dates/edit. A segment
// that starts otherwise, such as 222.json or %32%32, may hold an ID.
var trailingWords = regexp.MustCompile(`(/\d+)(?:/[A-Za-z][^/?#]*)+([?#]|$)`)

// Parsed represents components extracted from a Basecamp URL.
type Parsed struct {
	AccountID      string
	ProjectID      string // BucketID in Basecamp terminology
	Type           string // e.g., "todos", "messages", "cards"
	RecordingID    string
	CampfireID     string // parent campfire ID for chat-line URLs (chats/{campfireId}/lines/{lineId})
	PingID         string // the Ping (a Circle bucket) named by circles/{pingId} and circles/{pingId}@{lineId} URLs
	CommentID      string
	IsCollection   bool   // URL points to a list of items, not an individual resource
	OccurrenceDate string // date from schedule_entries/{id}/occurrences/{date} URLs
}

// router is the shared SDK router instance.
var router = basecamp.DefaultRouter()

// IsURL checks if the input looks like a Basecamp URL.
// Returns true if the URL can be matched (either as an API route or structurally).
func IsURL(input string) bool {
	normalized, _ := normalize(input)
	return router.Match(normalized) != nil
}

// normalize rewrites the web @-forms and itemPaths into routable URLs, and
// drops a .json suffix and the slashes Rails ignores. Chat lines become the
// slash form; a Ping line loses its @line, which the second result returns.
func normalize(input string) (normalized, pingLineID string) {
	normalized = chatAtRe.ReplaceAllString(squeezeSlashes(input), "${1}/lines/${2}")
	normalized = pingSidebarRe.ReplaceAllString(normalized, "${1}")
	if m := pingAtRe.FindStringSubmatch(normalized); m != nil {
		pingLineID = m[2]
		normalized = pingAtRe.ReplaceAllString(normalized, "/circles/${1}")
	}
	normalized = jsonSuffixRe.ReplaceAllString(normalized, "${1}${2}")
	for _, re := range itemPaths {
		normalized = re.ReplaceAllString(normalized, "${1}${2}")
	}
	return normalized, pingLineID
}

// squeezeSlashes collapses each run of slashes in a URL's path to one and drops
// a trailing slash, as Rails does when routing, so that a suffix or ID before
// them still ends the path.
func squeezeSlashes(rawURL string) string {
	end := strings.IndexAny(rawURL, "?#")
	if end == -1 {
		end = len(rawURL)
	}
	start := 0
	if i := strings.Index(rawURL[:end], "://"); i != -1 {
		start = i + len("://")
	} else if strings.HasPrefix(rawURL, "//") {
		start = len("//")
	}
	path := strings.TrimRight(slashRunRe.ReplaceAllString(rawURL[start:end], "/"), "/")
	return rawURL[:start] + path + rawURL[end:]
}

// urlPath returns the path the router matches: the URL without its scheme,
// host, query, or fragment.
func urlPath(rawURL string) string {
	path, _, _ := strings.Cut(rawURL, "#")
	path, _, _ = strings.Cut(path, "?")
	if _, afterScheme, ok := strings.Cut(path, "://"); ok {
		path = afterScheme
	} else {
		path = strings.TrimPrefix(path, "//")
	}
	if !strings.HasPrefix(path, "/") {
		slash := strings.Index(path, "/")
		if slash == -1 {
			return ""
		}
		path = path[slash:]
	}
	return path
}

// match routes a normalized URL, refusing a structural match that leaves an ID
// unconsumed. Words after the item's ID name the item: the URL is routed again
// without them, so a page of an API item, such as a chat line's edit page,
// still names that item.
func match(normalized string) *basecamp.Match {
	for _, u := range []string{normalized, trailingWords.ReplaceAllString(normalized, "${1}${2}")} {
		m := router.Match(u)
		if m == nil || m.Source == basecamp.MatchedAPI || structuralPath.MatchString(urlPath(u)) {
			return m
		}
	}
	return nil
}

// Parse extracts IDs from a Basecamp URL.
// Returns nil if the input is not a valid Basecamp URL, if its path goes past
// the item it names to another one Parse does not model, or if the ID it ends
// in is a person's or an embed's, so that callers refuse it rather than act on
// the wrong recording.
//
// Supported URL patterns:
//   - https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}
//   - https://3.basecamp.com/{account}/buckets/{bucket}/{type}/{id}#__recording_{comment}
//   - https://3.basecamp.com/{account}/buckets/{bucket}/card_tables/cards/{id}
//   - https://3.basecamp.com/{account}/buckets/{bucket}/chats/{id}@{lineId}
//   - https://3.basecamp.com/{account}/circles/{ping}
//   - https://3.basecamp.com/{account}/circles/{ping}@{lineId}
//   - https://3.basecamp.com/{account}/my/sidebar/circles/{ping}, with or without @{lineId}
//   - https://3.basecamp.com/{account}/buckets/{bucket}/schedule_entries/{id}/occurrences/{date}
//   - https://3.basecamp.com/{account}/buckets/{bucket}/vaults/{folder}/documents/{id}, and the
//     other children bc3 nests under folders, to-do lists, and questionnaires
//   - any of these followed by segments with no ID, such as /edit, and an upload
//     followed by /download/{filename} or a version's /download/{filename} or
//     /representations/{key}
//   - https://3.basecamp.com/{account}/projects/{project}
func Parse(input string) *Parsed {
	// Normalize the Basecamp web @-forms (chats/789@111, circles/456@111)
	// into routable URLs before routing.
	normalized, pingLineID := normalize(input)

	m := match(normalized)
	if m == nil {
		return nil
	}
	// questions/{id}/answers/by/{person}, people/{person}/out_of_office and the
	// reports by person end in a person's ID, and embeds/{sgid} in an sgid.
	if m.Params["sgid"] != "" || (m.Params["personId"] != "" && m.PathType != "people") {
		return nil
	}

	if pingLineID != "" {
		pingID := m.Params["pingId"]
		if pingID == "" {
			return nil
		}
		return &Parsed{
			AccountID:   m.AccountID,
			ProjectID:   pingID,
			Type:        "lines",
			RecordingID: pingLineID,
			PingID:      pingID,
			CommentID:   m.CommentID,
		}
	}

	pathType := m.PathType
	resourceID := m.ResourceID()

	// Occurrence URLs (.../schedule_entries/{id}/occurrences/{date}) resolve
	// to the parent schedule entry. The SDK router returns PathType="occurrences"
	// with the date as ResourceID. We rewrite to the parent entry but preserve
	// the date so callers (show, schedule show) can use the occurrence endpoint.
	// The "entryId" param name comes from the SDK's url-routes.json route table
	// entry for /{accountId}/schedule_entries/{entryId}/occurrences/{date}.
	var occurrenceDate string
	if pathType == "occurrences" {
		if entryID, ok := m.Params["entryId"]; ok && entryID != "" {
			occurrenceDate = resourceID // resourceID is the date segment
			pathType = "schedule_entries"
			resourceID = entryID
		}
	}

	// Detect collection URLs (e.g. .../todosets/777/todolists, .../chats/789/lines).
	// The SDK router matches these with the parent's ID as ResourceID and an
	// operation like "ListTodolists" or "CreateCardColumn" — never "Get*".
	// Structural matches for collection URLs already have empty ResourceID.
	isCollection := false
	if m.Source == basecamp.MatchedAPI && resourceID != "" {
		if getOp, hasGet := m.Operations["GET"]; hasGet {
			isCollection = strings.HasPrefix(getOp, "List")
		} else {
			// No GET operation (e.g. POST-only CreateCardColumn) — a collection route.
			isCollection = true
		}
	}

	// Chat-line URLs (.../chats/{campfireId}/lines/{lineId}) carry the parent
	// campfire ID as a path param. Surface it so callers can target the specific
	// campfire that owns the line rather than a project-default room. The
	// "campfireId" param name comes from the SDK's url-routes.json route table
	// entry for /{accountId}/chats/{campfireId}/lines/{lineId}. Non-chat-line
	// URLs simply lack this key — the map read then yields "" (a nil Params on
	// structural matches reads the same way), leaving CampfireID empty.
	campfireID := m.Params["campfireId"]

	return &Parsed{
		AccountID:      m.AccountID,
		ProjectID:      m.ProjectID,
		Type:           pathType,
		RecordingID:    resourceID,
		CampfireID:     campfireID,
		PingID:         m.Params["pingId"],
		CommentID:      m.CommentID,
		IsCollection:   isCollection,
		OccurrenceDate: occurrenceDate,
	}
}

// ExtractID extracts the primary ID from an argument.
// If the argument is a Basecamp URL, extracts the recording ID.
// Otherwise, returns the argument as-is (assumed to be an ID).
func ExtractID(arg string) string {
	if parsed := Parse(arg); parsed != nil {
		if parsed.RecordingID != "" {
			return parsed.RecordingID
		}
		if parsed.ProjectID != "" {
			return parsed.ProjectID
		}
		if parsed.AccountID != "" {
			return parsed.AccountID
		}
	}
	return arg
}

// ExtractProjectID extracts the project (bucket) ID from an argument.
// If the argument is a Basecamp URL, extracts the bucket ID.
// Otherwise, returns the argument as-is.
func ExtractProjectID(arg string) string {
	if parsed := Parse(arg); parsed != nil && parsed.ProjectID != "" {
		return parsed.ProjectID
	}
	return arg
}

// ExtractWithProject extracts both the recording ID and project ID from an argument.
// Returns (recordingID, projectID). If projectID is empty, it wasn't in the URL.
func ExtractWithProject(arg string) (recordingID, projectID string) {
	if parsed := Parse(arg); parsed != nil {
		return parsed.RecordingID, parsed.ProjectID
	}
	return arg, ""
}

// ExtractCommentWithProject extracts a comment ID and project ID from an argument.
// For URLs with a fragment (e.g., #__recording_789), returns the comment ID.
// For URLs without a fragment, returns the recording ID (same as ExtractWithProject).
// Returns (commentOrRecordingID, projectID).
func ExtractCommentWithProject(arg string) (id, projectID string) {
	if parsed := Parse(arg); parsed != nil {
		// Prefer comment ID from fragment when present
		if parsed.CommentID != "" {
			return parsed.CommentID, parsed.ProjectID
		}
		return parsed.RecordingID, parsed.ProjectID
	}
	return arg, ""
}

// ExtractIDs extracts IDs from multiple arguments, handling URLs and
// comma-separated values (e.g. "123,456,789").
func ExtractIDs(args []string) []string {
	var result []string
	for _, arg := range args {
		for part := range strings.SplitSeq(arg, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				result = append(result, ExtractID(part))
			}
		}
	}
	return result
}
