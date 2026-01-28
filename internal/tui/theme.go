// Package tui provides terminal user interface components.
package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ResolveTheme loads a theme with the following precedence:
//  1. NO_COLOR env var set → returns NoColorTheme (industry standard)
//  2. BCQ_THEME env var → parse custom colors.toml file
//  3. User theme from ~/.config/bcq/theme/colors.toml
//  4. Default bcq theme
//
// On systems like Omarchy, users can symlink to their system theme:
//
//	ln -s ~/.config/omarchy/current/theme ~/.config/bcq/theme
func ResolveTheme() Theme {
	// NO_COLOR support (industry standard for disabling colors)
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return NoColorTheme()
	}

	// BCQ_THEME allows custom theme file path
	if path := os.Getenv("BCQ_THEME"); path != "" {
		if theme, err := LoadThemeFromFile(path); err == nil {
			return theme
		}
		// Fall through on error
	}

	// Try user theme config
	if theme, err := LoadUserTheme(); err == nil {
		return theme
	}

	// Fall back to default
	return DefaultTheme()
}

// NoColorTheme returns a theme with empty colors (honors NO_COLOR standard).
// Lipgloss treats empty strings as "no color", resulting in plain text output.
func NoColorTheme() Theme {
	empty := lipgloss.AdaptiveColor{Light: "", Dark: ""}
	return Theme{
		Primary:    empty,
		Secondary:  empty,
		Success:    empty,
		Warning:    empty,
		Error:      empty,
		Muted:      empty,
		Background: empty,
		Foreground: empty,
		Border:     empty,
	}
}

// LoadUserTheme attempts to load a theme from the user's bcq config.
// The theme directory can be a symlink to another theme system.
func LoadUserTheme() (Theme, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Theme{}, err
	}

	path := filepath.Join(home, ".config", "bcq", "theme", "colors.toml")
	return LoadThemeFromFile(path)
}

// LoadThemeFromFile parses a colors.toml file and returns a Theme.
func LoadThemeFromFile(path string) (Theme, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: Path from trusted config
	if err != nil {
		return Theme{}, err
	}

	colors, err := parseSimpleTOML(data)
	if err != nil {
		return Theme{}, err
	}

	return mapColorsToTheme(colors), nil
}

// parseSimpleTOML parses a simple TOML file with key = "value" format.
// This is a lightweight parser for colors.toml theme files.
func parseSimpleTOML(data []byte) (map[string]string, error) { //nolint:unparam // error return for future extensibility
	result := make(map[string]string)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse key = "value" format
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue // Skip malformed lines
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Strip inline comments: "value" # comment -> "value"
		// Find comment marker outside quotes
		if idx := findInlineComment(value); idx > 0 {
			value = strings.TrimSpace(value[:idx])
		}

		// Remove quotes
		value = strings.Trim(value, `"'`)

		// Validate hex color format
		if !isValidHexColor(value) {
			continue
		}

		result[key] = value
	}

	return result, nil
}

// findInlineComment returns the index of an inline comment marker (#) that
// appears outside of quotes, or -1 if none found.
func findInlineComment(s string) int {
	inQuote := false
	quoteChar := rune(0)
	for i, c := range s {
		if !inQuote && (c == '"' || c == '\'') {
			inQuote = true
			quoteChar = c
		} else if inQuote && c == quoteChar {
			inQuote = false
		} else if !inQuote && c == '#' {
			return i
		}
	}
	return -1
}

// isValidHexColor checks if a string is a valid hex color (#RGB or #RRGGBB).
func isValidHexColor(s string) bool {
	if !strings.HasPrefix(s, "#") {
		return false
	}
	hex := s[1:]
	if len(hex) != 3 && len(hex) != 6 {
		return false
	}
	for _, c := range hex {
		isDigit := c >= '0' && c <= '9'
		isLower := c >= 'a' && c <= 'f'
		isUpper := c >= 'A' && c <= 'F'
		if !isDigit && !isLower && !isUpper {
			return false
		}
	}
	return true
}

// mapColorsToTheme maps colors.toml color names to bcq Theme semantics.
//
// Supported color keys (compatible with terminal theme formats):
//
//	accent = "#89b4fa"       → Primary
//	foreground = "#cdd6f4"   → Foreground
//	background = "#1e1e2e"   → Background
//	color0 = "#45475a"       → (black)
//	color1 = "#f38ba8"       → Error (red)
//	color2 = "#a6e3a1"       → Success (green)
//	color3 = "#f9e2af"       → Warning (yellow)
//	color4 = "#89b4fa"       → Primary fallback (blue)
//	color7 = "#bac2de"       → Secondary (white/light)
//	color8 = "#585b70"       → Muted, Border (bright black)
func mapColorsToTheme(colors map[string]string) Theme {
	defaults := DefaultTheme()

	// Helper to get color with fallbacks
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := colors[k]; ok {
				return v
			}
		}
		return ""
	}

	// Build theme from colors, falling back to defaults
	// Terminal themes are typically dark, so we populate Dark variants
	return Theme{
		Primary: lipgloss.AdaptiveColor{
			Light: defaults.Primary.Light,
			Dark:  getOrDefault(get("accent", "color4"), defaults.Primary.Dark),
		},
		Secondary: lipgloss.AdaptiveColor{
			Light: defaults.Secondary.Light,
			Dark:  getOrDefault(get("color7"), defaults.Secondary.Dark),
		},
		Success: lipgloss.AdaptiveColor{
			Light: defaults.Success.Light,
			Dark:  getOrDefault(get("color2"), defaults.Success.Dark),
		},
		Warning: lipgloss.AdaptiveColor{
			Light: defaults.Warning.Light,
			Dark:  getOrDefault(get("color3"), defaults.Warning.Dark),
		},
		Error: lipgloss.AdaptiveColor{
			Light: defaults.Error.Light,
			Dark:  getOrDefault(get("color1"), defaults.Error.Dark),
		},
		Muted: lipgloss.AdaptiveColor{
			Light: defaults.Muted.Light,
			Dark:  getOrDefault(get("color8", "color0"), defaults.Muted.Dark),
		},
		Background: lipgloss.AdaptiveColor{
			Light: defaults.Background.Light,
			Dark:  getOrDefault(get("background"), defaults.Background.Dark),
		},
		Foreground: lipgloss.AdaptiveColor{
			Light: defaults.Foreground.Light,
			Dark:  getOrDefault(get("foreground"), defaults.Foreground.Dark),
		},
		Border: lipgloss.AdaptiveColor{
			Light: defaults.Border.Light,
			Dark:  getOrDefault(get("color8", "color0"), defaults.Border.Dark),
		},
	}
}

// getOrDefault returns value if non-empty, otherwise returns defaultValue.
func getOrDefault(value, defaultValue string) string {
	if value != "" {
		return value
	}
	return defaultValue
}
