package commands

import "testing"

func TestIsUpdateAvailable(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "newer stable release", current: "1.0.0", latest: "1.1.0", want: true},
		{name: "same version", current: "1.0.0", latest: "1.0.0", want: false},
		{name: "older latest suppressed", current: "1.1.0", latest: "1.0.0", want: false},
		{name: "pseudo current newer than older release", current: "0.4.1-0.20260313174735-243815fa23b2", latest: "0.4.0", want: false},
		{name: "release newer than pseudo prerelease of same base", current: "0.4.1-0.20260313174735-243815fa23b2", latest: "0.4.1", want: true},
		{name: "invalid fallback treats different as update", current: "custom-build", latest: "1.0.0", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUpdateAvailable(tt.current, tt.latest); got != tt.want {
				t.Fatalf("isUpdateAvailable(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}
