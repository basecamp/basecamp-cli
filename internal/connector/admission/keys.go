package admission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// CheckCanonicalKeys walks a connect.json document and refuses any object
// that names a key twice or spells one noncanonically. Every reader of the
// file runs it, and it is exported so there is one implementation rather
// than one per reader: the whole point is that no two readers of the trust
// anchor can disagree about what a document says.
//
// The reason is encoding/json's field matching. It matches a struct tag
// case-insensitively and parses an integer map key with strconv, so to the
// decoder "Trust" is "trust", "PATH" is "path", and "01" is 1. A validation
// keyed on the canonical spelling therefore looks for a key the decoder has
// already matched under another one, and a value lands in a field nothing
// checked. Refusing the noncanonical spelling up front is what makes an
// exact comparison anywhere downstream a complete one.
//
// What that was worth here. Copilot on #765 reported one of these; the other
// five came out of looking at the seam the two readers share rather than at
// the field it named:
//
//	{"Trust":{"mode":"allowlist","allowlist_ids":[…]}}  trust mode escalated
//	{"Projects":{"2":{}}}                               a second served project merged in
//	{"projects":{"01":{}}}                              a served project setup refuses
//	{"projects":{"1":{"Path":null}}}                    the legacy-path check skipped
//	{"projects":{"1":{"PATH":"../x"}}}                  likewise, with a relative path
//
// The reported one is the fourth. Each of the others is the same mechanism
// wearing a different key, and the first is the trust anchor itself, which
// is why this is a document walk and not a check on "path".
func CheckCanonicalKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := keyTok.(string)
				if !canonicalKey(key) {
					return fmt.Errorf("key %q is not spelled canonically: names are lowercase, project ids plain decimal", key)
				}
				if seen[key] {
					return fmt.Errorf("key %q appears twice in one object", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
		}
		_, err = dec.Token() // the closing delimiter
		return err
	}
	return walk()
}

// canonicalKey reports whether a key is in its one canonical spelling:
// lowercase letters, digits and underscores for names, plain decimal for
// project ids.
func canonicalKey(key string) bool {
	if n, err := strconv.ParseInt(key, 10, 64); err == nil {
		return strconv.FormatInt(n, 10) == key
	}
	if key == "" {
		return false
	}
	for _, r := range key {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}
