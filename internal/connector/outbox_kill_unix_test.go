//go:build unix

package connector

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/basecamp/basecamp-sdk/go/pkg/basecamp"
)

// Done when: a kill between sending and the receipt, then a restart, yields
// exactly one message or an indeterminate intent — with a real process,
// killed by SIGKILL, not a simulated error.

const (
	obKillHelperEnv = "BASECAMP_CONNECT_OUTBOX_KILL_HELPER"
	obKillLedgerEnv = "BASECAMP_CONNECT_OUTBOX_KILL_LEDGER"
	obKillServerEnv = "BASECAMP_CONNECT_OUTBOX_KILL_SERVER"
	obKillMarkerEnv = "BASECAMP_CONNECT_OUTBOX_KILL_MARKER"
)

// TestOutboxKillHelperProcess is the process that gets killed. It does
// nothing unless started by the kill test.
func TestOutboxKillHelperProcess(t *testing.T) {
	if os.Getenv(obKillHelperEnv) == "" {
		t.Skip("helper process for the outbox kill test")
	}
	ledger, err := OpenLedger(os.Getenv(obKillLedgerEnv))
	require.NoError(t, err)
	client := basecamp.NewClient(&basecamp.Config{BaseURL: os.Getenv(obKillServerEnv)}, &basecamp.StaticTokenProvider{Token: "test-token-not-real"})
	poster, err := NewBasecampPoster(client.ForAccount("999"), adapterAgentID)
	require.NoError(t, err)

	var p Poster = poster
	if marker := os.Getenv(obKillMarkerEnv); marker != "" {
		// Stop between the committed sending row and the request.
		p = stallingPoster{Poster: poster, marker: marker}
	}
	ob, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: p})
	require.NoError(t, err)
	_ = ob.Flush(context.Background())
	select {} // never exits on its own: it is killed
}

type stallingPoster struct {
	Poster
	marker string
}

func (s stallingPoster) Post(context.Context, Destination, string) (int64, error) {
	_ = os.WriteFile(s.marker, []byte("sending"), 0o600)
	select {}
}

func TestOutboxKillBetweenSendingAndReceipt(t *testing.T) {
	cases := []struct {
		name string
		// landed: the request reached Basecamp before the kill.
		landed bool
	}{
		{name: "the request landed", landed: true},
		{name: "the request never left", landed: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state", "connector.db")
			ledger, err := OpenLedger(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = ledger.Close() })
			ledger.SetHooks(LifecycleHooks(ledger, LifecycleOptions{}))
			seenRecord(t, ledger, 1)
			_, err = ledger.Admission().Commit(ctx, obNoRouteVerdict(1, 0, obCommentReply))
			require.NoError(t, err)

			server := newOBServer(t)
			stored := make(chan struct{}, 1)
			release := make(chan struct{})
			server.setOnPost(func(r *http.Request, _ int64) int {
				// Answer nothing until the client is gone: the receipt never
				// reaches the process.
				stored <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return http.StatusServiceUnavailable
			})
			t.Cleanup(func() { close(release) })

			marker := filepath.Join(t.TempDir(), "sending")
			cmd := exec.CommandContext(context.WithoutCancel(ctx), os.Args[0], "-test.run=^TestOutboxKillHelperProcess$", "-test.count=1")
			cmd.Env = []string{
				obKillHelperEnv + "=1",
				obKillLedgerEnv + "=" + path,
				obKillServerEnv + "=" + server.URL,
				"HOME=" + os.Getenv("HOME"),
				"PATH=" + os.Getenv("PATH"),
			}
			if !tc.landed {
				cmd.Env = append(cmd.Env, obKillMarkerEnv+"="+marker)
			}
			require.NoError(t, cmd.Start())
			// Signaled through os.Process, which refuses a process already
			// reaped: the pid is never signaled after it could be reused.
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

			deadline := time.After(30 * time.Second)
			if tc.landed {
				select {
				case <-stored:
				case <-deadline:
					t.Fatal("the helper never made its request")
				}
			} else {
				for {
					if _, err := os.Stat(marker); err == nil {
						break
					}
					select {
					case <-deadline:
						t.Fatal("the helper never reached its request")
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			// The helper is between its durable sending row and a receipt.
			require.Equal(t, IntentSending, obIntent(t, ledger, holdingKey(1)).State)
			require.NoError(t, cmd.Process.Signal(syscall.SIGKILL))
			waitErr := cmd.Wait()
			var exitErr *exec.ExitError
			require.ErrorAs(t, waitErr, &exitErr)
			require.Equal(t, syscall.SIGKILL, exitErr.Sys().(syscall.WaitStatus).Signal())

			// Restart: a fresh outbox on the same ledger, Basecamp answering
			// normally now.
			server.setOnPost(nil)
			postsBefore := server.postCount()
			restarted, err := NewOutbox(OutboxOptions{Ledger: ledger, Poster: server.poster(t)})
			require.NoError(t, err)
			require.NoError(t, restarted.Recover(ctx))
			require.NoError(t, restarted.Flush(ctx))

			assert.Equal(t, postsBefore, server.postCount(), "the restart posted nothing")
			in := obIntent(t, ledger, holdingKey(1))
			messages := server.at(in.Destination)
			if tc.landed {
				require.Len(t, messages, 1, "exactly one message")
				require.Equal(t, IntentSent, in.State)
				assert.Equal(t, messages[0].ID, *in.ReceiptID)
			} else {
				assert.Empty(t, messages)
				assert.Equal(t, IntentIndeterminate, in.State, "never resent: a person decides")
			}
		})
	}
}
