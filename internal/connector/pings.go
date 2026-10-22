package connector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/basecamp/basecamp-cli/internal/connector/setup"
)

// PingsFile is where a profile's state keeps whether its connector hears the
// agent's Pings, beside the ledger.
//
// It is a file of its own rather than a ledger table or a connect.json key on
// purpose. A table is a schema migration, which an older build refuses to
// open; a connect.json key is one an older build's strict parse refuses, and
// connect.json is setup's to write, under the credential's lock. This file is
// the run's, written under the instance lock, and an older build never looks
// at it.
const PingsFile = "pings.json"

// PingsChoice is whether a profile's connector hears the agent's Pings, and
// what decided it. A profile's first start decides it and saves it, and every
// start after keeps it until --pings or --no-pings decides again.
type PingsChoice struct {
	Hears bool `json:"hears"`
	// Because is what decided it: --pings or --no-pings, or the default the
	// first start found for the profile's credential. The run names the
	// values; this file only keeps them.
	Because string    `json:"because"`
	At      time.Time `json:"at"`
}

// LoadPingsChoice reads the choice saved in a profile's state directory. It
// reports false when none has been saved. A file that is there and cannot be
// read as one is an error, never a default: guessing either way answers
// Pings twice or not at all.
func LoadPingsChoice(dir string) (PingsChoice, bool, error) {
	path := filepath.Join(dir, PingsFile)
	data, err := setup.ReadPrivateStateFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return PingsChoice{}, false, nil
	case err != nil:
		return PingsChoice{}, false, err
	}
	var raw struct {
		Hears   *bool     `json:"hears"`
		Because string    `json:"because"`
		At      time.Time `json:"at"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return PingsChoice{}, false, fmt.Errorf("%s cannot be read: %w", path, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return PingsChoice{}, false, fmt.Errorf("%s cannot be read: more than one object", path)
	}
	if raw.Hears == nil {
		return PingsChoice{}, false, fmt.Errorf("%s cannot be read: it does not say whether the connector hears Pings", path)
	}
	return PingsChoice{Hears: *raw.Hears, Because: raw.Because, At: raw.At}, true, nil
}

// SavePingsChoice saves the choice in a profile's state directory, replacing
// any earlier one. The run saves it while it holds the instance lock.
func SavePingsChoice(dir string, c PingsChoice) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return setup.WritePrivateStateFile(filepath.Join(dir, PingsFile), append(data, '\n'))
}
