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
// Then open http://localhost:9999 in your browser. Ctrl-C to stop.
func TestPreviewCallbackPages(t *testing.T) {
	if os.Getenv("PREVIEW") == "" {
		t.Skip("set PREVIEW=1 to run this preview server")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><style>
  body { font-family: system-ui; max-width: 400px; margin: 80px auto; }
  a { display: block; padding: 12px 0; font-size: 18px; }
</style></head><body>
  <h2>Callback page previews</h2>
  <a href="/success">Success page</a>
  <a href="/error">Error page</a>
</body></html>`)
	})
	mux.HandleFunc("/success", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, callbackSuccess)
	})
	mux.HandleFunc("/error", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, callbackError)
	})

	t.Log("Preview server running at http://localhost:9999")
	t.Log("  http://localhost:9999/success")
	t.Log("  http://localhost:9999/error")
	t.Log("Ctrl-C to stop")

	server := &http.Server{Addr: "127.0.0.1:9999", Handler: mux}
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
