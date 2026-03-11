package auth

import (
	"fmt"
	"net/http"
	"os"
	"testing"
)

// TestPreviewCallbackPages serves the callback pages for visual review.
//
//	PREVIEW=1 go test -run TestPreviewCallbackPages ./internal/auth/ -count=1
//
// Then open http://127.0.0.1:9999 in your browser. Ctrl-C to stop.
func TestPreviewCallbackPages(t *testing.T) {
	if os.Getenv("PREVIEW") == "" {
		t.Skip("set PREVIEW=1 to run this preview server")
	}

	pages := []struct {
		path, label, content string
	}{
		{"/success", "Success", callbackSuccess},
		{"/error", "Error", callbackError},
		{"/denied", "Denied", callbackDenied},
		{"/invalid", "Invalid / expired", callbackInvalid},
		{"/exchange-failed", "Exchange failed", callbackExchangeFailed},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><style>
  body { font-family: system-ui; max-width: 400px; margin: 80px auto; }
  a { display: block; padding: 12px 0; font-size: 18px; }
</style></head><body>
  <h2>Callback page previews</h2>`)
		for _, p := range pages {
			fmt.Fprintf(w, `  <a href="%s">%s</a>`+"\n", p.path, p.label)
		}
		fmt.Fprint(w, `</body></html>`)
	})
	for _, p := range pages {
		content := p.content
		mux.HandleFunc(p.path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, content)
		})
	}

	t.Log("Preview server running at http://127.0.0.1:9999")
	for _, p := range pages {
		t.Logf("  http://127.0.0.1:9999%s", p.path)
	}
	t.Log("Ctrl-C to stop")

	server := &http.Server{Addr: "127.0.0.1:9999", Handler: mux}
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
