package commands

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
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

// skillLoadPrefix opens the text block Claude Code writes into the
// transcript when a skill loads; the rest of its first line is the skill's
// own directory, whichever copy of basecamp-connect the session ran.
const skillLoadPrefix = "Base directory for this skill: "

// connectorSkillPrefilter skips decoding transcript lines that can't hold a
// skill load.
var connectorSkillPrefilter = []byte(skillLoadPrefix)

// transcriptRecord is the part of a transcript line a skill load lives in.
type transcriptRecord struct {
	Type    string `json:"type"`
	IsMeta  bool   `json:"isMeta"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

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
		"step you were on. The connector and any subagents in flight kept running through the compaction: don't " +
		"start them again. The request watcher exits on each request, so check whether it is still running; if it " +
		"has exited, read its output and start it again as the skill says."
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
			// A line longer than the buffer: read the rest of it whole. The
			// fragment is a view into the reader's buffer, which the next
			// read reuses, so it is copied first.
			head := append([]byte(nil), line...)
			rest, err2 := r.ReadBytes('\n')
			line = append(head, rest...)
			err = err2
		}
		if bytes.Contains(line, connectorSkillPrefilter) {
			if found := connectorSkillDirIn(line); found != "" {
				dir = found
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

// connectorSkillDirIn returns the basecamp-connect directory a transcript
// line loads, or "". Only a user record's own text block counts: a tool
// result or a message that quotes a skill load is not one.
func connectorSkillDirIn(line []byte) string {
	var record transcriptRecord
	if json.Unmarshal(line, &record) != nil || record.Type != "user" || !record.IsMeta {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(record.Message.Content, &blocks) != nil {
		return ""
	}
	var dir string
	for _, block := range blocks {
		rest, ok := strings.CutPrefix(block.Text, skillLoadPrefix)
		if block.Type != "text" || !ok {
			continue
		}
		first, _, _ := strings.Cut(rest, "\n")
		first = strings.TrimSpace(first)
		// Both separators: the transcript holds the path native to the
		// machine that wrote it.
		if strings.HasSuffix(first, "/basecamp-connect") || strings.HasSuffix(first, `\basecamp-connect`) {
			dir = first
		}
	}
	return dir
}
