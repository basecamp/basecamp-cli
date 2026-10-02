// Command fakebasecamp serves the fake Basecamp in
// internal/connector/fakebasecamp on a loopback port, with its default
// world, until it is interrupted: for running `basecamp connect` by hand
// with no Basecamp behind it.
//
//	go run ./e2e/fakebasecamp
//
// It prints the environment that points the CLI at it and the commands
// that connect and set up the default world's agent. While it runs, a line
// on standard input acts on the world: "mention" posts a comment that
// mentions the agent, on both lanes of the feed, and "drop" severs the live
// connections. Every request is printed as it is answered.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/basecamp/basecamp-cli/internal/connector/fakebasecamp"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:0", "loopback address to serve on")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("-listen: %v", err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		// The fake authorizes nothing for real, and the CLI only speaks
		// plain HTTP and ws:// to a loopback host anyway.
		log.Fatalf("-listen: %s is not a loopback address", host)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	r := &logReporter{}
	s := fakebasecamp.Start(r, fakebasecamp.DefaultWorld(), fakebasecamp.WithListener(ln))
	defer r.cleanup()

	fmt.Printf(`fake Basecamp at %[1]s

  export BASECAMP_BASE_URL=%[1]s BASECAMP_OAUTH_ISSUER=%[1]s BASECAMP_NO_KEYRING=1
  basecamp auth agent connect -P agent --no-browser
  basecamp connect setup -P agent --operator %[2]d --serve %[3]d
  basecamp connect -P agent

Type "mention" to post a comment that mentions the agent, "drop" to sever
the live connections. Ctrl-C stops the fake.
`, s.URL(), fakebasecamp.OperatorID, fakebasecamp.ProjectID)

	go readCommands(s)
	go printRequests(ctx, s)
	<-ctx.Done()
}

// printRequests prints each request once it is answered, in arrival order.
func printRequests(ctx context.Context, s *fakebasecamp.Server) {
	printed := 0
	for {
		var requests []fakebasecamp.Request
		answered := func() bool {
			requests = s.Requests()
			return printed < len(requests) && requests[printed].Status != 0
		}
		if s.Await(ctx, answered) != nil {
			return
		}
		for ; printed < len(requests) && requests[printed].Status != 0; printed++ {
			r := requests[printed]
			fmt.Printf("%s %s %d\n", r.Method, r.Path, r.Status)
		}
	}
}

// readCommands acts on each line of standard input.
func readCommands(s *fakebasecamp.Server) {
	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		switch strings.TrimSpace(lines.Text()) {
		case "":
		case "mention":
			fmt.Printf("emitted event %d\n", mention(s).ID)
		case "drop":
			fmt.Printf("dropped %d live connections\n", s.DropCable())
		default:
			fmt.Println(`unknown command; try "mention" or "drop"`)
		}
	}
}

// mentions counts the comments mention has added. Only readCommands calls
// mention, one line at a time, so it needs no lock.
var mentions int64

// mention adds a comment by the operator that mentions the agent, on a
// message it adds the first time, and publishes the comment's event on both
// lanes.
func mention(s *fakebasecamp.Server) fakebasecamp.Event {
	const message int64 = 1_000_000
	mentions++
	id := message + mentions
	s.Update(func(w *fakebasecamp.World) {
		if _, ok := w.Recordings[message]; !ok {
			w.Recordings[message] = &fakebasecamp.Recording{
				ID: message, Type: "Message", BucketID: fakebasecamp.ProjectID, CreatorID: fakebasecamp.OperatorID, Title: "Kickoff",
			}
		}
		w.Recordings[id] = &fakebasecamp.Recording{
			ID: id, Type: "Comment", BucketID: fakebasecamp.ProjectID, ParentID: message, CreatorID: fakebasecamp.OperatorID,
			Content: "<p>Could you take a look, " + fakebasecamp.Mention(fakebasecamp.AgentID) + "?</p>",
		}
	})
	return s.Emit(fakebasecamp.Event{
		EventType: "comment.created", BucketID: fakebasecamp.ProjectID, RecordingID: id, CreatorID: fakebasecamp.OperatorID,
	}, fakebasecamp.Both)
}

// logReporter reports to the log, and runs the fake's cleanup at exit.
type logReporter struct {
	mu       sync.Mutex
	cleanups []func()
}

func (*logReporter) Helper() {}

func (*logReporter) Errorf(format string, args ...any) { log.Printf(format, args...) }

func (r *logReporter) Cleanup(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cleanups = append(r.cleanups, fn)
}

func (r *logReporter) cleanup() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
}
