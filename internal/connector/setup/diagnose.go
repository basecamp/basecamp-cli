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

var (
	unknownFieldPattern = regexp.MustCompile(`unknown field "([^"]*)"`)
	nonCanonicalPattern = regexp.MustCompile(`key "([^"]*)" is not spelled canonically`)
	duplicateKeyPattern = regexp.MustCompile(`key "([^"]*)" appears twice in one object`)
)

// suggestKey adds, to a refusal of a key, where that key is in data and, for
// one that is misspelled, the key it was probably meant to be: one valid in
// the object it sits in. within is the path of the object the key was refused
// in, when the refusal says (a project's entry); with none, the first
// appearance of the key where the schema does not allow it is named. Any other
// error is returned as it is.
func suggestKey(data []byte, err error, within ...string) error {
	msg := err.Error()
	var path []string
	if len(within) > 0 {
		path = within
	}
	var at int
	var near string
	switch {
	case unknownFieldPattern.MatchString(msg):
		key := unknownFieldPattern.FindStringSubmatch(msg)[1]
		var parents []string
		at, parents = findKey(data, func(ps []string, k string, _ map[string]bool) bool {
			if k != key || (path != nil && !slices.Equal(ps, path)) {
				return false
			}
			t := typeAt(ps)
			return t == nil || !keysOf(t, true)[k]
		})
		if at >= 0 {
			near = closestKey(key, keysOf(typeAt(parents), false))
		}
	case nonCanonicalPattern.MatchString(msg):
		key := nonCanonicalPattern.FindStringSubmatch(msg)[1]
		var parents []string
		at, parents = findKey(data, func(_ []string, k string, _ map[string]bool) bool { return k == key })
		if at >= 0 {
			known := keysOf(typeAt(parents), false)
			if lower := strings.ToLower(key); known[lower] {
				near = lower
			} else {
				near = closestKey(key, known)
			}
		}
	case duplicateKeyPattern.MatchString(msg):
		key := duplicateKeyPattern.FindStringSubmatch(msg)[1]
		at, _ = findKey(data, func(_ []string, k string, seen map[string]bool) bool { return k == key && seen[k] })
	default:
		return err
	}
	if at >= 0 {
		line, col := lineColumn(data, at)
		msg = fmt.Sprintf("line %d, column %d: %s", line, col, msg)
	}
	if near != "" {
		msg += fmt.Sprintf("; did you mean %q?", near)
	}
	return errors.New(msg)
}

// findKey is the offset and the enclosing keys of the first key in data that
// match accepts, -1 when none does. match is also given the keys already
// seen in the same object.
func findKey(data []byte, match func(parents []string, key string, seen map[string]bool) bool) (int, []string) {
	at, where := -1, []string(nil)
	seen := map[string]map[string]bool{}
	walkKeys(data, func(parents []string, k string, offset int) bool {
		// An object is named by its keys and its offset-free path; two
		// sibling objects in one array share it, which only makes a
		// duplicate easier to find, never one that is not there.
		id := strings.Join(parents, "\x00")
		if seen[id] == nil {
			seen[id] = map[string]bool{}
		}
		if match(parents, k, seen[id]) {
			at, where = offset, parents
			return false
		}
		seen[id][k] = true
		return true
	})
	return at, where
}

// keyOffset is where key appears in data as a key of the object at path
// (nil: any object, the first), -1 when it does not.
func keyOffset(data []byte, key string, path []string) int {
	at, _ := findKey(data, func(parents []string, k string, _ map[string]bool) bool {
		return k == key && (path == nil || slices.Equal(parents, path))
	})
	return at
}

// typeAt is the Go type connect.json decodes the object at path into, nil
// where the path leaves the schema. An array element adds "" to a path.
func typeAt(path []string) reflect.Type {
	t := reflect.TypeFor[File]()
	for _, part := range path {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		switch t.Kind() {
		case reflect.Map, reflect.Slice, reflect.Array:
			t = t.Elem()
		case reflect.Struct:
			field, ok := fieldByKey(t, part)
			if !ok {
				return nil
			}
			t = field.Type
		default:
			return nil
		}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func fieldByKey(t reflect.Type, key string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); f.IsExported() && name == key {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// keysOf is the keys an object of type t may carry, legacy keys only when
// asked: they are accepted, and nobody should be told to write one. A type
// that is not a struct (a map keyed by id) has none.
func keysOf(t reflect.Type, legacy bool) map[string]bool {
	keys := map[string]bool{}
	if t == nil || t.Kind() != reflect.Struct {
		return keys
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() || (!legacy && strings.HasPrefix(f.Name, "Legacy")) {
			continue
		}
		if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
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

// closestKey is the key in known nearest to key, or "" when none is near
// enough to be a likely misspelling: at most two edits, and fewer than half
// its length.
func closestKey(key string, known map[string]bool) string {
	names := make([]string, 0, len(known))
	for k := range known {
		names = append(names, k)
	}
	slices.Sort(names)
	best, bestDist := "", 3
	for _, k := range names {
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
