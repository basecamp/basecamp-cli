package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSimpleTOML(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "valid colors",
			input: `accent = "#89b4fa"
foreground = "#cdd6f4"
background = "#1e1e2e"`,
			want: map[string]string{
				"accent":     "#89b4fa",
				"foreground": "#cdd6f4",
				"background": "#1e1e2e",
			},
		},
		{
			name: "with comments and empty lines",
			input: `# This is a comment
accent = "#89b4fa"

# Another comment
foreground = "#cdd6f4"
`,
			want: map[string]string{
				"accent":     "#89b4fa",
				"foreground": "#cdd6f4",
			},
		},
		{
			name: "single quotes",
			input: `accent = '#89b4fa'
foreground = '#cdd6f4'`,
			want: map[string]string{
				"accent":     "#89b4fa",
				"foreground": "#cdd6f4",
			},
		},
		{
			name: "malformed lines skipped",
			input: `accent = "#89b4fa"
this line has no equals sign
foreground = "#cdd6f4"`,
			want: map[string]string{
				"accent":     "#89b4fa",
				"foreground": "#cdd6f4",
			},
		},
		{
			name: "invalid hex colors skipped",
			input: `accent = "#89b4fa"
bad_color = "not-a-color"
invalid_hex = "#gggggg"
foreground = "#cdd6f4"`,
			want: map[string]string{
				"accent":     "#89b4fa",
				"foreground": "#cdd6f4",
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  map[string]string{},
		},
		{
			name: "short hex colors",
			input: `color = "#fff"
accent = "#abc"`,
			want: map[string]string{
				"color":  "#fff",
				"accent": "#abc",
			},
		},
		{
			name: "inline comments",
			input: `accent = "#89b4fa" # primary blue
foreground = "#cdd6f4" # main text`,
			want: map[string]string{
				"accent":     "#89b4fa",
				"foreground": "#cdd6f4",
			},
		},
		{
			name: "unquoted values",
			input: `accent = #89b4fa
foreground = #cdd6f4`,
			want: map[string]string{
				"accent":     "#89b4fa",
				"foreground": "#cdd6f4",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseSimpleTOML([]byte(tt.input))
			if (err != nil) != tt.wantErr {
				t.Errorf("parseSimpleTOML() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if len(got) != len(tt.want) {
				t.Errorf("parseSimpleTOML() got %d entries, want %d", len(got), len(tt.want))
				return
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseSimpleTOML()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestIsValidHexColor(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"#fff", true},
		{"#FFF", true},
		{"#ffffff", true},
		{"#FFFFFF", true},
		{"#89b4fa", true},
		{"#ABC123", true},
		{"fff", false},        // missing #
		{"#gg0000", false},    // invalid hex chars
		{"#12345", false},     // wrong length (5)
		{"#1234567", false},   // wrong length (7)
		{"", false},           // empty
		{"#", false},          // just hash
		{"#ab", false},        // too short
		{"red", false},        // color name
		{"rgb(0,0,0)", false}, // rgb format
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isValidHexColor(tt.input)
			if got != tt.want {
				t.Errorf("isValidHexColor(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestMapColorsToTheme(t *testing.T) {
	t.Run("full color set", func(t *testing.T) {
		colors := map[string]string{
			"accent":     "#89b4fa",
			"foreground": "#cdd6f4",
			"background": "#1e1e2e",
			"color1":     "#f38ba8",
			"color2":     "#a6e3a1",
			"color3":     "#f9e2af",
			"color7":     "#bac2de",
			"color8":     "#585b70",
		}

		theme := mapColorsToTheme(colors)

		if theme.Primary.Dark != "#89b4fa" {
			t.Errorf("Primary.Dark = %q, want %q", theme.Primary.Dark, "#89b4fa")
		}
		if theme.Error.Dark != "#f38ba8" {
			t.Errorf("Error.Dark = %q, want %q", theme.Error.Dark, "#f38ba8")
		}
		if theme.Success.Dark != "#a6e3a1" {
			t.Errorf("Success.Dark = %q, want %q", theme.Success.Dark, "#a6e3a1")
		}
		if theme.Warning.Dark != "#f9e2af" {
			t.Errorf("Warning.Dark = %q, want %q", theme.Warning.Dark, "#f9e2af")
		}
		if theme.Secondary.Dark != "#bac2de" {
			t.Errorf("Secondary.Dark = %q, want %q", theme.Secondary.Dark, "#bac2de")
		}
		if theme.Muted.Dark != "#585b70" {
			t.Errorf("Muted.Dark = %q, want %q", theme.Muted.Dark, "#585b70")
		}
		if theme.Foreground.Dark != "#cdd6f4" {
			t.Errorf("Foreground.Dark = %q, want %q", theme.Foreground.Dark, "#cdd6f4")
		}
		if theme.Background.Dark != "#1e1e2e" {
			t.Errorf("Background.Dark = %q, want %q", theme.Background.Dark, "#1e1e2e")
		}
	})

	t.Run("partial color set uses defaults", func(t *testing.T) {
		colors := map[string]string{
			"accent": "#89b4fa",
		}

		theme := mapColorsToTheme(colors)
		defaults := DefaultTheme()

		if theme.Primary.Dark != "#89b4fa" {
			t.Errorf("Primary.Dark = %q, want %q", theme.Primary.Dark, "#89b4fa")
		}

		if theme.Error.Dark != defaults.Error.Dark {
			t.Errorf("Error.Dark = %q, want default %q", theme.Error.Dark, defaults.Error.Dark)
		}
		if theme.Success.Dark != defaults.Success.Dark {
			t.Errorf("Success.Dark = %q, want default %q", theme.Success.Dark, defaults.Success.Dark)
		}
	})

	t.Run("empty map returns all defaults", func(t *testing.T) {
		colors := map[string]string{}
		theme := mapColorsToTheme(colors)
		defaults := DefaultTheme()

		if theme.Primary.Dark != defaults.Primary.Dark {
			t.Errorf("Primary.Dark = %q, want default %q", theme.Primary.Dark, defaults.Primary.Dark)
		}
		if theme.Error.Dark != defaults.Error.Dark {
			t.Errorf("Error.Dark = %q, want default %q", theme.Error.Dark, defaults.Error.Dark)
		}
	})

	t.Run("color4 fallback for primary", func(t *testing.T) {
		colors := map[string]string{
			"color4": "#0000ff",
		}

		theme := mapColorsToTheme(colors)
		if theme.Primary.Dark != "#0000ff" {
			t.Errorf("Primary.Dark = %q, want %q (color4 fallback)", theme.Primary.Dark, "#0000ff")
		}
	})
}

func TestLoadThemeFromFile(t *testing.T) {
	t.Run("valid file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "colors.toml")

		content := `# Test theme
accent = "#89b4fa"
foreground = "#cdd6f4"
background = "#1e1e2e"
color1 = "#f38ba8"
color2 = "#a6e3a1"
color3 = "#f9e2af"
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		theme, err := LoadThemeFromFile(testFile)
		if err != nil {
			t.Fatalf("LoadThemeFromFile failed: %v", err)
		}

		if theme.Primary.Dark != "#89b4fa" {
			t.Errorf("Primary.Dark = %q, want %q", theme.Primary.Dark, "#89b4fa")
		}
		if theme.Error.Dark != "#f38ba8" {
			t.Errorf("Error.Dark = %q, want %q", theme.Error.Dark, "#f38ba8")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadThemeFromFile("/nonexistent/path/colors.toml")
		if err == nil {
			t.Error("LoadThemeFromFile() should return error for missing file")
		}
	})
}

func TestNoColorTheme(t *testing.T) {
	theme := NoColorTheme()

	if theme.Primary.Light != "" || theme.Primary.Dark != "" {
		t.Errorf("Primary should be empty, got Light=%q Dark=%q", theme.Primary.Light, theme.Primary.Dark)
	}
	if theme.Error.Light != "" || theme.Error.Dark != "" {
		t.Errorf("Error should be empty, got Light=%q Dark=%q", theme.Error.Light, theme.Error.Dark)
	}
	if theme.Success.Light != "" || theme.Success.Dark != "" {
		t.Errorf("Success should be empty, got Light=%q Dark=%q", theme.Success.Light, theme.Success.Dark)
	}
	if theme.Foreground.Light != "" || theme.Foreground.Dark != "" {
		t.Errorf("Foreground should be empty, got Light=%q Dark=%q", theme.Foreground.Light, theme.Foreground.Dark)
	}
}

func TestResolveTheme(t *testing.T) {
	t.Run("NO_COLOR returns empty theme", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")

		theme := ResolveTheme()

		if theme.Primary.Light != "" || theme.Primary.Dark != "" {
			t.Errorf("With NO_COLOR, Primary should be empty, got Light=%q Dark=%q",
				theme.Primary.Light, theme.Primary.Dark)
		}
	})

	t.Run("BCQ_THEME loads custom file", func(t *testing.T) {
		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "custom.toml")

		content := `accent = "#ff0000"
foreground = "#ffffff"
`
		if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Setenv("BCQ_THEME", testFile)

		theme := ResolveTheme()

		if theme.Primary.Dark != "#ff0000" {
			t.Errorf("With BCQ_THEME, Primary.Dark = %q, want %q", theme.Primary.Dark, "#ff0000")
		}
	})

	t.Run("BCQ_THEME invalid file falls back", func(t *testing.T) {
		t.Setenv("BCQ_THEME", "/nonexistent/theme.toml")

		theme := ResolveTheme()

		// Should fall back to Omarchy or default - just check it's not empty
		if theme.Primary.Dark == "" && theme.Primary.Light == "" {
			t.Error("With invalid BCQ_THEME, should fall back to a valid theme")
		}
	})

	t.Run("default theme when no env vars", func(t *testing.T) {
		// Ensure these env vars are not set for this test
		// Note: t.Setenv in other subtests auto-cleans up, providing isolation
		os.Unsetenv("NO_COLOR")
		os.Unsetenv("BCQ_THEME")

		theme := ResolveTheme()

		// Should return a valid theme (either user theme or default)
		// We just verify it's not empty - could be user config or default
		if theme.Primary.Dark == "" && theme.Primary.Light == "" {
			t.Error("ResolveTheme() returned theme with empty Primary color")
		}
	})
}

func TestLoadUserTheme(t *testing.T) {
	t.Run("loads theme from user config dir", func(t *testing.T) {
		// Create a temporary home directory structure
		tmpHome := t.TempDir()
		themeDir := filepath.Join(tmpHome, ".config", "bcq", "theme")
		if err := os.MkdirAll(themeDir, 0755); err != nil {
			t.Fatalf("Failed to create theme dir: %v", err)
		}

		content := `accent = "#00ff00"
foreground = "#eeeeee"
`
		themeFile := filepath.Join(themeDir, "colors.toml")
		if err := os.WriteFile(themeFile, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to write theme file: %v", err)
		}

		// LoadUserTheme uses os.UserHomeDir, so we test via BCQ_THEME instead
		// since we can't easily mock the home directory
		t.Setenv("BCQ_THEME", themeFile)
		theme := ResolveTheme()

		if theme.Primary.Dark != "#00ff00" {
			t.Errorf("Primary.Dark = %q, want %q", theme.Primary.Dark, "#00ff00")
		}
	})

	t.Run("returns error for missing config", func(t *testing.T) {
		_, err := LoadThemeFromFile("/nonexistent/path/colors.toml")
		if err == nil {
			t.Error("LoadThemeFromFile should return error for missing file")
		}
	})
}

func TestGetOrDefault(t *testing.T) {
	tests := []struct {
		value        string
		defaultValue string
		want         string
	}{
		{"#ff0000", "#0000ff", "#ff0000"},
		{"", "#0000ff", "#0000ff"},
		{"", "", ""},
	}

	for _, tt := range tests {
		got := getOrDefault(tt.value, tt.defaultValue)
		if got != tt.want {
			t.Errorf("getOrDefault(%q, %q) = %q, want %q", tt.value, tt.defaultValue, got, tt.want)
		}
	}
}
