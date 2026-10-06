package auth

import (
	"os"
	"path/filepath"
	"strings"
)

// Logins in containers.
//
// A BC5 login refreshes by rotation: every refresh replaces its refresh
// token, and a replaced one presented again outside a short grace window is
// read as stolen, and the whole login is signed out — the second half of
// the theft defense rotation exists for. So one login can be used from many
// processes only if they share it, taking turns under the credential lock
// (lock.go), and never if they each hold a copy: the first copy to refresh
// replaces the token, the next presents the replaced one, and every copy is
// signed out at once.
//
// Copies are what containers make. A credentials.json baked into an image,
// or copied into each replica's own volume, is one login in N places, and
// it is revoked the first time two of them refresh. The CLI cannot see a
// copy, so it says so where copies come from: once, on the first refresh
// in a container, and again in the message a revoked login gets there.

// inContainer reports whether this process appears to run in a container:
// Docker's /.dockerenv, Podman's /run/.containerenv, the `container`
// variable systemd-nspawn and Podman set, or a Kubernetes service
// environment. A miss only costs a notice. A test seam.
var inContainer = func() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return os.Getenv("container") != "" || os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

// containerLoginAdvice is what to do instead of copying a login between
// containers: share one, or use a credential that does not rotate.
const containerLoginAdvice = "Give every container the same config directory as a shared volume (they take turns refreshing it), " +
	"or set BASECAMP_TOKEN to a personal access token, which never rotates; never copy credentials.json between containers"

// revokedForReuse reports whether an invalid_grant's description is bc3's
// answer to a login signed out because a replaced refresh token came back:
// "Token reuse detected, session terminated" the first time, "Token has
// been revoked" every time after (app/models/oauth/refresh_token/rotation.rb,
// app/models/oauth/refresh_token.rb). It chooses only the words of the
// message. Whether the login is forgotten is decided by the error code
// alone (invalidGrant), so a reworded description costs a sentence, not a
// login.
func revokedForReuse(desc string) bool {
	return desc == "Token has been revoked" || strings.HasPrefix(desc, "Token reuse detected")
}

// revokedLoginMessage is the message for a BC5 login signed out for reuse;
// in a container, containerLoginAdvice follows it. The usual cause is named
// because it is the one a person can stop doing; "probably", because a login
// revoked by a sign-out elsewhere reads the same.
const revokedLoginMessage = "This login was revoked, probably because it was used from more than one place: " +
	"each refresh replaces its token, and Basecamp signs a login out when a replaced token comes back"

// containerNoticeFile marks a config directory whose container has been
// told once not to copy its login. It lives beside the credential locks, so
// containers sharing one directory share one notice.
const containerNoticeFile = "container-login-notice"

// noticeContainerLogin tells a container, once, that the login it just
// refreshed must not be copied into other containers, and what to do
// instead. Only a login that refreshes is at risk — an agent's mint and an
// imported token do not rotate — and a scheduled job must not print it on
// every run, so a marker in the config directory records it was said. A
// marker that cannot be written only means it is said again next time.
func (m *Manager) noticeContainerLogin(creds *Credentials) {
	if creds.RefreshToken == "" || m.store == nil || m.store.fallbackDir == "" || !inContainer() {
		return
	}
	marker := filepath.Join(m.store.fallbackDir, containerNoticeFile)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	m.warnf("notice: this login refreshes by replacing its token, so if it is copied into other containers, "+
		"the first two copies to refresh get every copy signed out. %s.", containerLoginAdvice)
	_ = os.WriteFile(marker, nil, 0o600)
}
