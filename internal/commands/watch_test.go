package commands

import (
	"fmt"
	"strings"
	"testing"
	"time"

	actioncable "github.com/basecamp/actioncable-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/output"
)

// testWatch reports every kind, with the notifications an arrival is described
// from already read.
func testWatch(notifications ...basecamp.Notification) *readingsWatch {
	watch := &readingsWatch{latest: map[int64]basecamp.Notification{}}
	for _, notification := range notifications {
		watch.latest[notification.ID] = notification
	}

	return watch
}

func testNotification(id int64, section string) basecamp.Notification {
	return basecamp.Notification{
		ID: id, Section: section, Type: "Comment",
		Title: "Re: Launch plan", BucketName: "Ops",
		Creator:      &basecamp.Person{Name: "Jason Fried"},
		ReadableSGID: "BAh7CEkiCG\u2026",
		AppURL:       "https://app.basecamp.com/2914079/buckets/21350690/comments/3576722222",
		CreatedAt:    time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC),
	}
}

// --- What counts as an arrival ---

// A notification already read when the watch first saw it is a backlog, not
// news.
func TestABacklogOfReadNotificationsIsNotNews(t *testing.T) {
	watch := testWatch(testNotification(1, "inbox"), testNotification(2, "inbox"))

	arrivals := watch.arrivalsBetween(
		map[int64]seenReading{},
		map[int64]seenReading{1: {}, 2: {}})

	assert.Empty(t, arrivals)
}

func TestAnArrivalIsReported(t *testing.T) {
	watch := testWatch(testNotification(7, "inbox"))

	arrivals := watch.arrivalsBetween(
		map[int64]seenReading{},
		map[int64]seenReading{7: {unread: true, count: 1}})

	require.Len(t, arrivals, 1)
	assert.Equal(t, "comment", arrivals[0].Type)
	assert.Equal(t, int64(7), arrivals[0].NotificationID)
	assert.Equal(t, "Jason Fried", arrivals[0].Creator)
	assert.Equal(t, "Ops", arrivals[0].Project)
}

// An agent has to act on what arrived, so the identifiers come out of the
// app_url rather than leaving it to be taken apart downstream.
func TestAnArrivalCarriesWhatToActOn(t *testing.T) {
	watch := testWatch(testNotification(7, "inbox"))

	arrivals := watch.arrivalsBetween(
		map[int64]seenReading{},
		map[int64]seenReading{7: {unread: true, count: 1}})

	require.Len(t, arrivals, 1)
	assert.Equal(t, int64(21350690), arrivals[0].BucketID)
	assert.Equal(t, int64(3576722222), arrivals[0].RecordingID)
	assert.Equal(t, "BAh7CEkiCG…", arrivals[0].SGID)
	assert.Equal(t, "Comment", arrivals[0].BasecampType)
}

// A second reply on a thread the reader already has an unread for is news
// again: the row stays the same, only its count moves.
func TestMoreActivityOnAKnownUnreadIsNewsAgain(t *testing.T) {
	watch := testWatch(testNotification(7, "inbox"))

	arrivals := watch.arrivalsBetween(
		map[int64]seenReading{7: {unread: true, count: 1}},
		map[int64]seenReading{7: {unread: true, count: 2}})

	require.Len(t, arrivals, 1)
	assert.Equal(t, int32(2), arrivals[0].UnreadCount)

	// A count that stayed put is not.
	assert.Empty(t, watch.arrivalsBetween(
		map[int64]seenReading{7: {unread: true, count: 2}},
		map[int64]seenReading{7: {unread: true, count: 2}}))
}

// Marking something read is something the reader did, not something that
// happened to them, so it is not reported at all.
func TestGoingReadIsNotAnEvent(t *testing.T) {
	watch := testWatch(testNotification(7, "inbox"))

	assert.Empty(t, watch.arrivalsBetween(
		map[int64]seenReading{7: {unread: true, count: 1}},
		map[int64]seenReading{7: {count: 1}}))

	// Nor is one that vanished from the read altogether.
	assert.Empty(t, watch.arrivalsBetween(
		map[int64]seenReading{7: {unread: true, count: 1}},
		map[int64]seenReading{}))
}

// Two arrivals in one read come out in a settled order rather than a map's.
func TestArrivalsComeOutInAStableOrder(t *testing.T) {
	watch := testWatch(testNotification(3, "inbox"), testNotification(1, "pings"), testNotification(2, "inbox"))

	arrivals := watch.arrivalsBetween(
		map[int64]seenReading{},
		map[int64]seenReading{3: {unread: true}, 1: {unread: true}, 2: {unread: true}})

	require.Len(t, arrivals, 3)
	assert.Equal(t, []int64{1, 2, 3},
		[]int64{arrivals[0].NotificationID, arrivals[1].NotificationID, arrivals[2].NotificationID})
}

// The endpoint answers with at most 100 unreads while pruning waits until 200,
// so past 100 a reader's oldest unreads are missing from the response rather
// than read. Forgetting them would report each one again as an arrival the next
// time it came back into the window.
func TestUnreadsOutsideTheResponseWindowAreNotForgotten(t *testing.T) {
	older := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)

	watch := &readingsWatch{seen: map[int64]seenReading{
		1: {unread: true, count: 1, unreadAt: older},
	}}
	found := map[int64]seenReading{2: {unread: true, count: 1, unreadAt: newer}}

	watch.keepWhatFellOutsideTheWindow(found, maxUnreadWindow)

	assert.Contains(t, found, int64(1), "an unread pushed out of the window was forgotten")
	assert.Equal(t, older, found[1].unreadAt)
}

// A response that did not fill the window proves what is missing from it, so
// nothing is carried forward.
func TestAShortResponseForgetsWhatIsGone(t *testing.T) {
	watch := &readingsWatch{seen: map[int64]seenReading{
		1: {unread: true, count: 1, unreadAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)},
	}}
	found := map[int64]seenReading{}

	watch.keepWhatFellOutsideTheWindow(found, 7)

	assert.Empty(t, found)
}

// --- What kind a notification is ---

// The kinds are named for whoever reads them, and two of them need Basecamp's
// section as well as its type to tell apart.
func TestTheKindANotificationIsReportedAs(t *testing.T) {
	kind := func(basecampType, section string) string {
		return watchTypeOf(basecamp.Notification{Type: basecampType, Section: section})
	}

	assert.Equal(t, "mention", kind("Mention", "inbox"))
	assert.Equal(t, "comment", kind("Comment", "inbox"))
	assert.Equal(t, "assignment", kind("Assignment", "inbox"))
	assert.Equal(t, "boost", kind("BoostReport", "inbox"))

	// A chat in a circle is a direct or group message; one in a project is not.
	assert.Equal(t, "ping", kind("Chat", "pings"))
	assert.Equal(t, "chat", kind("Chat", "chats"))

	// Anything Basecamp decided to show again is a bubble, whatever it is.
	assert.Equal(t, "bubble", kind("Chat::Lines", "bubbles"))
	assert.Equal(t, "bubble", kind("Message", "bubbles"))
}

// A kind this version has no name for is passed through rather than dropped: an
// agent told about something it does not recognize can still act on it.
func TestAnUnknownKindPassesThrough(t *testing.T) {
	assert.Equal(t, "somethingnew", watchTypeOf(basecamp.Notification{Type: "SomethingNew", Section: "inbox"}))
}

// --- What gets reported ---

// No --types reports every kind, including one this version cannot name.
func TestWithoutTypesEveryKindIsReported(t *testing.T) {
	watch := &readingsWatch{}

	assert.True(t, watch.reporting(watchEvent{Type: "mention"}))
	assert.True(t, watch.reporting(watchEvent{Type: "somethingnew"}))
}

func TestTypesNarrowWhatIsReported(t *testing.T) {
	watch := &readingsWatch{types: map[string]bool{"mention": true, "ping": true}}

	assert.True(t, watch.reporting(watchEvent{Type: "mention"}))
	assert.True(t, watch.reporting(watchEvent{Type: "ping"}))
	assert.False(t, watch.reporting(watchEvent{Type: "chat"}))
	assert.False(t, watch.reporting(watchEvent{Type: "comment"}))
}

func TestUnknownTypesAreRefused(t *testing.T) {
	_, err := (&watchCommand{types: []string{"mention", "nonsense"}}).watchedTypes()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonsense")

	// Nothing asked for is everything, not an error.
	wanted, err := (&watchCommand{types: nil}).watchedTypes()
	require.NoError(t, err)
	assert.Nil(t, wanted)

	wanted, err = (&watchCommand{types: []string{" MENTION ", "ping"}}).watchedTypes()
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"mention": true, "ping": true}, wanted)
}

// --- Exit on first ---

func TestExitOnFirstStopsAfterOne(t *testing.T) {
	watch := &readingsWatch{exitOnFirst: true}
	assert.False(t, watch.finished())

	watch.reported = 1
	assert.True(t, watch.finished())

	// Without it, a watch keeps going.
	assert.False(t, (&readingsWatch{reported: 9}).finished())
}

// --- What a script is handed ---

func TestTheEnvironmentAScriptSees(t *testing.T) {
	event := watchEvent{
		Type: "ping", At: "2026-09-03T08:00:00.000Z", NotificationID: 7,
		BucketID: 21350690, RecordingID: 3576722222, SGID: "BAh7CEkiCG",
		URL: "https://app.basecamp.com/1/2", BasecampType: "Chat", Section: pingsSection,
		Title: "Re: Launch plan", Project: "Ops", Creator: "Jason Fried", UnreadCount: 3,
	}

	environment := map[string]string{}
	for _, variable := range event.environment() {
		name, value, _ := strings.Cut(variable, "=")
		environment[name] = value
	}

	assert.Equal(t, "ping", environment["BASECAMP_TYPE"])
	assert.Equal(t, "Chat", environment["BASECAMP_BASECAMP_TYPE"])
	assert.Equal(t, "7", environment["BASECAMP_NOTIFICATION_ID"])
	assert.Equal(t, "21350690", environment["BASECAMP_BUCKET_ID"])
	assert.Equal(t, "3576722222", environment["BASECAMP_RECORDING_ID"])
	assert.Equal(t, "BAh7CEkiCG", environment["BASECAMP_SGID"])
	assert.Equal(t, "pings", environment["BASECAMP_SECTION"])
	assert.Equal(t, "Ops", environment["BASECAMP_PROJECT"])
	assert.Equal(t, "Jason Fried", environment["BASECAMP_CREATOR"])
	assert.Equal(t, "3", environment["BASECAMP_UNREAD_COUNT"])

	// What an event does not carry it does not set, so a script never reads a
	// value left over from something else.
	plain := map[string]string{}
	for _, variable := range (watchEvent{Type: watchReady}).environment() {
		name, value, _ := strings.Cut(variable, "=")
		plain[name] = value
	}
	assert.Equal(t, "ready", plain["BASECAMP_TYPE"])
	assert.NotContains(t, plain, "BASECAMP_PROJECT")
	assert.NotContains(t, plain, "BASECAMP_BUCKET_ID")
}

// A variable already in the environment would reach a script for an event that
// does not set it — a watch started by another watch's script, say.
func TestStaleWatchVariablesAreStripped(t *testing.T) {
	kept := withoutWatchVariables([]string{
		"PATH=/usr/bin", "BASECAMP_SGID=stale", "BASECAMP_PROJECT=Somewhere else", "HOME=/home/x",
	})

	assert.Equal(t, []string{"PATH=/usr/bin", "HOME=/home/x"}, kept)
}

// --- The human line ---

func TestTheLineAnArrivalIsReportedAs(t *testing.T) {
	line := watchLine(watchEvent{
		Type: "ping", At: "2026-09-03T08:00:00.000Z", NotificationID: 7,
		Title: "Re: Launch plan", Project: "Ops", Creator: "Jason Fried",
	})

	assert.Contains(t, line, "ping")
	assert.Contains(t, line, "Ops")
	assert.Contains(t, line, "Jason Fried — Re: Launch plan")
}

func TestTheWatchsOwnNewsReadsAsWords(t *testing.T) {
	assert.Contains(t, watchLine(watchEvent{Type: watchReady}), "watching for notifications")
	assert.Contains(t, watchLine(watchEvent{Type: watchDisconnected}), "connection lost")
}

func TestALongTitleIsCutRatherThanWrapped(t *testing.T) {
	assert.Equal(t, "abc\u2026", truncateLine("abcdefgh", 4))
	assert.Equal(t, "abc", truncateLine("abc", 4))
	assert.Equal(t, "R\u00e9\u2026", truncateLine("R\u00e9staurant", 3), "a cut fell inside a rune")
}

// --- Heartbeats ---

// A refused heartbeat is fatal, not a warning: an offline connection is sent
// nothing at all.
func TestARefusedHeartbeatSaysWhatItCosts(t *testing.T) {
	err := heartbeatError(fmt.Errorf("%w: AppearanceChannel", actioncable.ErrRejected))

	assert.Equal(t, output.CodeForbidden, output.AsError(err).Code)
	assert.Contains(t, err.Error(), "never be sent any notifications")
}

// --- Which cable server ---

func TestADevelopmentBaseURLIsRecognised(t *testing.T) {
	assert.True(t, isLocalBaseURL("http://3.basecamp.localhost:3001"))
	assert.True(t, isLocalBaseURL("http://127.0.0.1:3001"))
	assert.False(t, isLocalBaseURL("https://3.basecampapi.com"))
	assert.False(t, isLocalBaseURL(""))
}

// A time nothing happened at says nothing rather than the zero instant.
func TestWatchTime(t *testing.T) {
	assert.Equal(t, "2026-09-03T08:00:00.000Z",
		watchTime(time.Date(2026, 9, 3, 8, 0, 0, 0, time.UTC)))
	assert.Empty(t, watchTime(time.Time{}))
}

// A change is stamped with whichever transition time the notification carries,
// which is the field its list was ordered by.
func TestWhenAChangeHappened(t *testing.T) {
	created := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	unreadAt := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	readAt := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)

	assert.Equal(t, unreadAt, notificationTime(basecamp.Notification{CreatedAt: created, UnreadAt: &unreadAt, ReadAt: &readAt}))
	assert.Equal(t, readAt, notificationTime(basecamp.Notification{CreatedAt: created, ReadAt: &readAt}))
	assert.Equal(t, created, notificationTime(basecamp.Notification{CreatedAt: created}))
}
