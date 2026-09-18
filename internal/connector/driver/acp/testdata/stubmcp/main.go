// stubmcp is a minimal stdio MCP server for the ACP adapter-compatibility
// test, ported from the card 23 spike. It records what it was started with and
// what it was asked, so a check can tell "spawned" from "spawned and
// connected", and see what the agent sent its one tool.
//
// Data minimization: the record holds the value of only the probe variables
// named on its command line, which the test sets to dummy values. Every other
// variable is recorded by name only, so a real credential in the environment
// it inherited never reaches the record.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type record struct {
	PID       int               `json:"pid"`
	ProbeVars map[string]string `json:"probe_vars"`
	// Fingerprints are SHA-256 digests of the variables named by
	// --fingerprint: enough to tell whose value a variable carries, without
	// the value.
	Fingerprints map[string]string `json:"fingerprints"`
	EnvVarNames  []string          `json:"env_var_names"`
	Methods      []string          `json:"methods"`
	Notes        []string          `json:"notes"`
}

var (
	mu   sync.Mutex
	rec  record
	path string
)

func main() {
	probes := flag.String("probe", "", "comma-separated variable names whose values may be recorded")
	fingerprints := flag.String("fingerprint", "", "comma-separated variable names whose values are recorded as digests")
	flag.StringVar(&path, "record", "", "where to write the record")
	flag.Parse()

	rec.PID = os.Getpid()
	rec.ProbeVars = map[string]string{}
	for _, name := range strings.Split(*probes, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if v, ok := os.LookupEnv(name); ok {
			rec.ProbeVars[name] = v
		}
	}
	rec.Fingerprints = map[string]string{}
	for _, name := range strings.Split(*fingerprints, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if v, ok := os.LookupEnv(name); ok {
			sum := sha256.Sum256([]byte(v))
			rec.Fingerprints[name] = hex.EncodeToString(sum[:])
		}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		rec.EnvVarNames = append(rec.EnvVarNames, name)
	}
	sort.Strings(rec.EnvVarNames)
	flush()
	serve()
}

func flush() {
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(&rec, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, path)
	}
}

func serve() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 16<<20)
	out := bufio.NewWriter(os.Stdout)
	reply := func(id json.RawMessage, result any) {
		if len(id) == 0 {
			return
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		_, _ = out.Write(append(data, '\n'))
		_ = out.Flush()
	}
	for in.Scan() {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &m) != nil {
			continue
		}
		mu.Lock()
		rec.Methods = append(rec.Methods, m.Method)
		flush()
		mu.Unlock()

		switch m.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(m.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			reply(m.ID, map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "acp-compat-stub", "version": "0.1.0"},
			})
		case "tools/list":
			reply(m.ID, map[string]any{"tools": []any{map[string]any{
				"name":        "note",
				"description": "Records a short note for the test harness.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			}}})
		case "tools/call":
			var p struct {
				Arguments struct {
					Text string `json:"text"`
				} `json:"arguments"`
			}
			_ = json.Unmarshal(m.Params, &p)
			mu.Lock()
			rec.Notes = append(rec.Notes, p.Arguments.Text)
			flush()
			mu.Unlock()
			reply(m.ID, map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "noted at " + time.Now().UTC().Format(time.RFC3339)}},
				"isError": false,
			})
		case "ping":
			reply(m.ID, map[string]any{})
		case "resources/list":
			reply(m.ID, map[string]any{"resources": []any{}})
		case "prompts/list":
			reply(m.ID, map[string]any{"prompts": []any{}})
		default:
			reply(m.ID, map[string]any{})
		}
	}
}
