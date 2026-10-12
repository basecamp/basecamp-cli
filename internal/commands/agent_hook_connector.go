package commands

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// After a compaction Claude Code re-attaches each invoked skill cut to its
// first 5,000 tokens, oldest dropped once they pass 25,000 together
// (code.claude.com/docs/en/skills, "Skill content lifecycle"). The
// basecamp-connect skill is longer than that, and what falls past the cut is
// what its orchestrating session needs on every request: the handoff steps
// and the subagent's instructions. A hook's additionalContext is capped at
// 10,000 characters, too small for the skill, so the hook names the file to
// read instead. It runs on every compaction, so unlike a reminder written
// into the skill it can't be dropped with it.

// connectorSkillLoad matches the line Claude Code writes into the transcript
// when a skill loads, as a text block of its own (a tool result quoting the
// line escapes its quotes and never matches). The path is the skill's own
// directory, whichever copy of basecamp-connect the session ran.
var connectorSkillLoad = regexp.MustCompile(`"text":"Base directory for this skill: ([^"\\]+/basecamp-connect)\\n`)

// connectorSkillPrefilter skips the regexp on transcript lines that can't match.
var connectorSkillPrefilter = []byte(`Base directory for this skill: `)

// connectorReloadNotice tells a session that ran basecamp-connect, right after
// it compacts, to read the skill again in full. Any other session, a
// SessionStart for any other reason, or a skill file no longer on disk, gets
// "".
func connectorReloadNotice(input agentHookInput) string {
	if input.Source != "compact" || input.TranscriptPath == "" {
		return ""
	}
	dir := lastConnectorSkillDir(input.TranscriptPath)
	if dir == "" {
		return ""
	}
	skill := filepath.Join(dir, "SKILL.md")
	if info, err := os.Stat(skill); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return "This session ran the Basecamp connector skill (basecamp-connect), and the compaction that just " +
		"finished cut that skill to its first 5,000 tokens, which drops the handoff steps and the subagent's " +
		"instructions. If this session is still the connector's orchestrator, read " + skill + " in full with " +
		"the Read tool now, before you go on with anything, a request in hand included, then carry on from the " +
		"step you were on. The connector, its monitor and any subagents in flight kept running through the " +
		"compaction: don't start them again."
}

// lastConnectorSkillDir returns the directory of the last basecamp-connect
// skill load in the transcript, or "" for none or an unreadable transcript.
// Transcripts of long-lived sessions run to hundreds of megabytes, so it
// streams them a line at a time.
func lastConnectorSkillDir(path string) string {
	f, err := os.Open(path) //nolint:gosec // G304: the transcript path Claude Code hands its own hook
	if err != nil {
		return ""
	}
	defer f.Close()
	var dir string
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull {
			// A line longer than the buffer: read the rest of it whole.
			rest, err2 := r.ReadBytes('\n')
			line = append(append([]byte(nil), line...), rest...)
			err = err2
		}
		if bytes.Contains(line, connectorSkillPrefilter) {
			for _, m := range connectorSkillLoad.FindAllSubmatch(line, -1) {
				dir = strings.TrimSpace(string(m[1]))
			}
		}
		if err == io.EOF {
			return dir
		}
		if err != nil {
			return ""
		}
	}
}
