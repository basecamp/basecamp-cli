package workspace

import (
	"context"
	"sync"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"

	"github.com/basecamp/basecamp-cli/internal/appctx"
	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/recents"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/data"
)

// Session holds the active workspace state: auth, SDK access, scope, and styles.
type Session struct {
	app        *appctx.App
	scope      Scope
	recents    *recents.Store
	styles     *tui.Styles
	multiStore *data.MultiStore
	hub        *data.Hub

	// Deep-link: initial navigation target set via CLI args.
	initialTarget *ViewTarget
	initialScope  *Scope

	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
	epoch  uint64
}

// NewSession creates a session from the fully-initialized App.
func NewSession(app *appctx.App) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	ms := data.NewMultiStore(app.SDK)
	s := &Session{
		app:        app,
		styles:     tui.NewStylesWithTheme(tui.ResolveTheme()),
		multiStore: ms,
		hub:        data.NewHub(ms, data.NewPoller()),
		ctx:        ctx,
		cancel:     cancel,
	}

	// Initialize scope from config
	s.scope.AccountID = app.Config.AccountID

	// Initialize recents store
	if app.Config.CacheDir != "" {
		s.recents = recents.NewStore(app.Config.CacheDir)
	}

	return s
}

// App returns the underlying appctx.App.
func (s *Session) App() *appctx.App {
	return s.app
}

// Scope returns the current scope.
// Thread-safe: may be called from Cmd goroutines.
func (s *Session) Scope() Scope {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scope
}

// SetScope updates the current scope.
func (s *Session) SetScope(scope Scope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scope = scope
}

// Styles returns the current TUI styles.
func (s *Session) Styles() *tui.Styles {
	return s.styles
}

// Recents returns the recents store (may be nil if no cache dir).
func (s *Session) Recents() *recents.Store {
	return s.recents
}

// AccountClient returns the SDK client for the current account.
// Panics if AccountID is not set — call RequireAccount first.
// Thread-safe: reads scope under lock.
func (s *Session) AccountClient() *basecamp.AccountClient {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.app.SDK.ForAccount(s.scope.AccountID)
}

// HasAccount returns true if an account is selected.
// Thread-safe: may be called from Cmd goroutines.
func (s *Session) HasAccount() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scope.AccountID != ""
}

// MultiStore returns the cross-account data layer.
func (s *Session) MultiStore() *data.MultiStore {
	return s.multiStore
}

// Hub returns the central data coordinator for typed, realm-scoped pool access.
func (s *Session) Hub() *data.Hub {
	return s.hub
}

// Context returns the session's cancellable context for SDK operations.
// Canceled on account switch or shutdown, aborting in-flight requests.
// Thread-safe: may be called from Cmd goroutines concurrently with ResetContext.
func (s *Session) Context() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ctx
}

// Epoch returns the session's monotonic epoch counter.
// Incremented on every account switch; used by the workspace to discard
// stale async results that were initiated under a previous account.
// Thread-safe: may be called from Cmd goroutines.
func (s *Session) Epoch() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.epoch
}

// ResetContext cancels the current context (aborting in-flight operations),
// creates a fresh one, and advances the epoch counter. Called on account switch.
func (s *Session) ResetContext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel()
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.epoch++
}

// NewTestSession returns a minimal Session for use in external package tests.
// It provides styles and an empty MultiStore (no accounts discovered),
// but no app, hub, or recents.
func NewTestSession() *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		styles:     tui.NewStyles(),
		multiStore: data.NewMultiStore(nil),
		ctx:        ctx,
		cancel:     cancel,
	}
}

// NewTestSessionWithHub returns a test Session that includes a Hub.
// The Hub's MultiStore has nil SDK, so ClientFor returns nil and Hub
// mutation methods return an error — but the Hub itself is non-nil,
// which is enough for key handler tests that exercise the state machine.
func NewTestSessionWithHub() *Session {
	s := NewTestSession()
	s.hub = data.NewHub(s.multiStore, data.NewPoller())
	return s
}

// NewTestSessionWithScope returns a test Session with a Hub and a pre-set scope.
func NewTestSessionWithScope(scope Scope) *Session {
	s := NewTestSessionWithHub()
	s.scope = scope
	return s
}

// NewTestSessionWithRecents is like NewTestSession but includes a recents store.
func NewTestSessionWithRecents(r *recents.Store) *Session {
	s := NewTestSession()
	s.recents = r
	return s
}

// SetInitialView configures a deep-link target to navigate to on startup
// instead of Home. Called from the tui command when a URL argument is provided.
func (s *Session) SetInitialView(target ViewTarget, scope Scope) {
	s.initialTarget = &target
	s.initialScope = &scope
}

// ConsumeInitialView returns and clears the deep-link target, if any.
// Returns (target, scope, true) when a deep-link was set, or (0, {}, false) otherwise.
func (s *Session) ConsumeInitialView() (ViewTarget, Scope, bool) {
	if s.initialTarget == nil {
		return 0, Scope{}, false
	}
	target := *s.initialTarget
	scope := *s.initialScope
	s.initialTarget = nil
	s.initialScope = nil
	return target, scope, true
}

// ReloadTheme re-reads the theme from disk and updates the shared Styles
// in place. All components holding *Styles see new colors on the next render.
func (s *Session) ReloadTheme() {
	theme := tui.ResolveTheme()
	s.styles.UpdateTheme(theme)
}

// Shutdown cancels the session context and tears down all Hub realms.
// Called on program exit.
func (s *Session) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel()
	if s.hub != nil {
		s.hub.Shutdown()
	}
}
