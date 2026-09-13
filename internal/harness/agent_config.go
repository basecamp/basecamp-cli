package harness

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type relativeConfigPathPolicy bool

const (
	rejectRelativeConfigPath  relativeConfigPathPolicy = false
	resolveRelativeConfigPath relativeConfigPathPolicy = true
)

func resolveAgentConfigDir(envName, defaultDir string, relativePolicy relativeConfigPathPolicy) (string, error) {
	configured := os.Getenv(envName)
	if configured != "" && configured != "~" && !strings.HasPrefix(configured, "~/") && !strings.HasPrefix(configured, "~\\") {
		if filepath.IsAbs(configured) {
			return filepath.Clean(configured), nil
		}
		if relativePolicy == rejectRelativeConfigPath {
			return "", fmt.Errorf(`%s must be an absolute path, ~, or start with ~/ or ~\`, envName)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolving relative %s: %w", envName, err)
		}
		return filepath.Join(cwd, configured), nil
	}

	home, err := UserHomeDir()
	if err != nil {
		return "", err
	}
	if configured == "" {
		return filepath.Join(home, defaultDir), nil
	}
	if configured == "~" || configured == "~/" || configured == "~\\" {
		return home, nil
	}
	return filepath.Join(home, configured[2:]), nil
}

// UserHomeDir returns the absolute home required for global agent paths.
func UserHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("getting home directory: empty path")
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("getting home directory: path must be absolute: %s", home)
	}
	return filepath.Clean(home), nil
}
