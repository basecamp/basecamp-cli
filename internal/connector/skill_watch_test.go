package connector

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// skillCommand returns the one line of the skill that starts with prefix,
// with the output file placeholder pointed at out.
func skillCommand(t *testing.T, prefix, out string) string {
	t.Helper()
	var found []string
	for line := range strings.SplitSeq(readConnectSkill(t, "SKILL.md"), "\n") {
		if strings.HasPrefix(line, prefix) {
			found = append(found, line)
		}
	}
	require.Len(t, found, 1, "the skill has one line starting %q", prefix)
	return strings.ReplaceAll(found[0], "<connector-output-file>", out)
}

func requestLine(id int64) string {
	return fmt.Sprintf(`{"type":"request","event_id":%d,"event_type":"comment_created","trigger":"mentioned"}`, id)
}

func TestTheConnectSkillsWatchFilterSkipsOnlyHandledRequests(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk on PATH")
	}
	out := filepath.Join(t.TempDir(), "connector.output")
	filter := skillCommand(t, "awk -v hf=", out)
	record := skillCommand(t, `printf '"event_id":%s,\n' <event_id>`, out)

	lines := []string{
		`{"level":"info","msg":"connector: running"}`,
		`{"type":"event","event_id":5,"reason":"untrusted_performer"}`,
		requestLine(101),
		requestLine(1011),
	}
	require.NoError(t, os.WriteFile(out, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	run := func() string {
		got, err := exec.CommandContext(context.Background(), "sh", "-c", filter+" '"+out+"'").Output()
		require.NoError(t, err)
		return string(got)
	}

	assert.Equal(t, requestLine(101)+"\n"+requestLine(1011)+"\n", run(),
		"with no handled file yet, every request line passes and nothing else does")

	require.NoError(t, exec.CommandContext(context.Background(), "sh", "-c",
		strings.Replace(record, "<event_id>", "101", 1)).Run())
	assert.Equal(t, requestLine(1011)+"\n", run(),
		"a handled request is skipped, and one whose id merely starts with it is not")
}

// The monitor reads the filter's output as it streams. A filter that holds
// its output until its input ends never fires, which is how a `grep -v`
// under Claude Code's own grep failed.
func TestTheConnectSkillsWatchFilterStreamsEachLine(t *testing.T) {
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("no awk on PATH")
	}
	out := filepath.Join(t.TempDir(), "connector.output")
	filter := skillCommand(t, "awk -v hf=", out)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", filter)
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	_, err = fmt.Fprintln(stdin, requestLine(7))
	require.NoError(t, err)

	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		assert.Equal(t, requestLine(7)+"\n", line)
	case <-time.After(5 * time.Second):
		t.Fatal("the filter held a request line back while its input stayed open")
	}
}
