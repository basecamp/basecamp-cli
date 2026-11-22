package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// A connect.json someone typed into has to say where it went wrong: a
// person can fix "line 7, column 3" and cannot fix "invalid character 'n'".
// These turn the decoder's errors into ones that name a line and column, and
// a misspelled key into the key it was probably meant to be. They change what
// a refusal says, never whether the file is refused.

// locate adds the line and column where a decoder error happened in data, for
// a syntax error, a value of the wrong type and a file that ends early. Any
// other error is returned as it is.
//
// A syntax error's own offset cannot be used: one met by Decoder.Token counts
// from the start of the value being read, not of the file. Where data is not
// valid JSON, the position comes from a scan of the whole document instead,
// which counts from its start.
func locate(data []byte, err error) error {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		var whole *json.SyntaxError
		if errors.As(json.Unmarshal(data, new(json.RawMessage)), &whole) {
			if whole.Error() == "unexpected end of JSON input" {
				line, col := lineColumn(data, len(data))
				return fmt.Errorf("line %d, column %d: the file ends before everything it opens is closed (a missing } or ]?)", line, col)
			}
			// The offset counts the byte that stopped the scan.
			return positioned(data, int(whole.Offset)-1, whole)
		}
		return err
	case errors.As(err, &typ):
		// The offset is just past the value; its last byte is on its line.
		return positioned(data, int(typ.Offset)-1, err)
	}
	return err
}

func positioned(data []byte, at int, err error) error {
	if at < 0 || at > len(data) {
		return err
	}
	line, col := lineColumn(data, at)
	return fmt.Errorf("line %d, column %d: %w", line, col, err)
}

// lineColumn is the 1-based line and column of the byte at index at in data.
func lineColumn(data []byte, at int) (int, int) {
	before := data[:at]
	return bytes.Count(before, []byte("\n")) + 1, at - bytes.LastIndexByte(before, '\n')
}

var unknownFieldPattern = regexp.MustCompile(`unknown field "([^"]*)"`)

// suggestKey adds, to an unknown-key refusal, where the key is in data and
// the known key it is closest to, when one is close enough to be the key
// meant. within is the path of the object the key was refused in, when the
// refusal says (a project's entry); with none, the key's first appearance is
// named. Any other error is returned as it is.
func suggestKey(data []byte, err error, within ...string) error {
	m := unknownFieldPattern.FindStringSubmatch(err.Error())
	if m == nil {
		return err
	}
	key := m[1]
	msg := err.Error()
	if offset := keyOffset(data, key, within); offset >= 0 {
		line, col := lineColumn(data, offset)
		msg = fmt.Sprintf("line %d, column %d: %s", line, col, msg)
	}
	if near := closestKey(key); near != "" {
		msg += fmt.Sprintf("; did you mean %q?", near)
	}
	return errors.New(msg)
}

// locateIn places an error met decoding the value at path, whose offsets
// count from that value's start rather than the file's.
func locateIn(data []byte, path []string, err error) error {
	var typ *json.UnmarshalTypeError
	if !errors.As(err, &typ) {
		return err
	}
	start := valueOffset(data, path)
	if start < 0 {
		return err
	}
	return positioned(data, start+int(typ.Offset)-1, err)
}

// keyOffset is where key appears in data as a key of the object at path
// (nil: any object, the first), -1 when it does not. It walks the tokens, so
// a value that happens to spell the key is not mistaken for it.
func keyOffset(data []byte, key string, path []string) int {
	at := -1
	walkKeys(data, func(parents []string, k string, offset int) bool {
		if k == key && (path == nil || slices.Equal(parents, path)) {
			at = offset
			return false
		}
		return true
	})
	return at
}

// valueOffset is where the value of the key path names begins in data, -1
// when there is none.
func valueOffset(data []byte, path []string) int {
	if len(path) == 0 {
		return -1
	}
	at := -1
	walkKeys(data, func(parents []string, k string, offset int) bool {
		if k != path[len(path)-1] || !slices.Equal(parents, path[:len(path)-1]) {
			return true
		}
		rest := data[offset:]
		if colon := bytes.IndexByte(rest, ':'); colon >= 0 {
			v := colon + 1
			for v < len(rest) && (rest[v] == ' ' || rest[v] == '\t' || rest[v] == '\n' || rest[v] == '\r') {
				v++
			}
			at = offset + v
		}
		return false
	})
	return at
}

// walkKeys calls visit with each object key in data, the keys of the objects
// it sits in, and the offset of its opening quote, until visit returns false
// or the document ends or stops parsing. Array elements add no key.
func walkKeys(data []byte, visit func(parents []string, key string, offset int) bool) {
	type frame struct {
		object, wantKey bool
		key             string // the key whose value this container is, "" in an array
	}
	var stack []frame
	pending := "" // the key just read, whose value comes next
	valueRead := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].wantKey = true
		}
	}
	parents := func() []string {
		p := make([]string, 0, len(stack)-1)
		for _, f := range stack[1:] {
			p = append(p, f.key)
		}
		return p
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				valueRead()
				stack = append(stack, frame{object: d == '{', wantKey: d == '{', key: pending})
				pending = ""
			default:
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if n := len(stack); n > 0 && stack[n-1].wantKey {
			stack[n-1].wantKey = false
			k, _ := tok.(string)
			pending = k
			// The offset before the token can sit ahead of a comma and
			// spaces; the key starts at its quote.
			offset := int(start)
			if i := bytes.IndexByte(data[start:], '"'); i >= 0 {
				offset += i
			}
			if !visit(parents(), k, offset) {
				return
			}
			continue
		}
		pending = ""
		valueRead()
	}
}

// knownKeys is every key connect.json may carry, at any depth, gathered from
// the json tags of the types Parse decodes into, so it cannot fall behind
// them. The legacy keys a file may still have are left out: nobody should be
// told to write one.
var knownKeys = func() []string {
	seen := map[string]bool{}
	var walk func(t reflect.Type)
	visited := map[reflect.Type]bool{}
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer || t.Kind() == reflect.Map || t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || visited[t] {
			return
		}
		visited[t] = true
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() || strings.HasPrefix(field.Name, "Legacy") {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name != "" && name != "-" {
				seen[name] = true
			}
			walk(field.Type)
		}
	}
	walk(reflect.TypeFor[File]())
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}()

// closestKey is the known key nearest to key, or "" when none is near enough
// to be a likely misspelling: at most two edits, and fewer than half its
// length.
func closestKey(key string) string {
	best, bestDist := "", 3
	for _, k := range knownKeys {
		if k == key {
			return ""
		}
		d := editDistance(strings.ToLower(key), k)
		if d < bestDist && d*2 < len(k) {
			best, bestDist = k, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b, in bytes: keys
// are ASCII.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
