package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, len(tt.want), len(got))
			for k, v := range tt.want {
				assert.Equal(t, v, got[k], "key %q", k)
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
			assert.Equal(t, tt.want, got)
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

		assert.Equal(t, "#89b4fa", theme.Primary.Dark)
		assert.Equal(t, "#f38ba8", theme.Error.Dark)
		assert.Equal(t, "#a6e3a1", theme.Success.Dark)
		assert.Equal(t, "#f9e2af", theme.Warning.Dark)
		assert.Equal(t, "#bac2de", theme.Secondary.Dark)
		assert.Equal(t, "#585b70", theme.Muted.Dark)
		assert.Equal(t, "#cdd6f4", theme.Foreground.Dark)
		assert.Equal(t, "#1e1e2e", theme.Background.Dark)
	})

	t.Run("partial color set uses defaults", func(t *testing.T) {
		colors := map[string]string{
			"accent": "#89b4fa",
		}

		theme := mapColorsToTheme(colors)
		defaults := DefaultTheme()

		assert.Equal(t, "#89b4fa", theme.Primary.Dark)
		assert.Equal(t, defaults.Error.Dark, theme.Error.Dark)
		assert.Equal(t, defaults.Success.Dark, theme.Success.Dark)
	})

	t.Run("empty map returns all defaults", func(t *testing.T) {
		colors := map[string]string{}
		theme := mapColorsToTheme(colors)
		defaults := DefaultTheme()

		assert.Equal(t, defaults.Primary.Dark, theme.Primary.Dark)
		assert.Equal(t, defaults.Error.Dark, theme.Error.Dark)
	})

	t.Run("color4 fallback for primary", func(t *testing.T) {
		colors := map[string]string{
			"color4": "#0000ff",
		}

		theme := mapColorsToTheme(colors)
		assert.Equal(t, "#0000ff", theme.Primary.Dark, "color4 fallback")
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
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err, "Failed to write test file")

		theme, err := LoadThemeFromFile(testFile)
		require.NoError(t, err)

		assert.Equal(t, "#89b4fa", theme.Primary.Dark)
		assert.Equal(t, "#f38ba8", theme.Error.Dark)
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadThemeFromFile("/nonexistent/path/colors.toml")
		assert.Error(t, err, "LoadThemeFromFile() should return error for missing file")
	})
}

func TestNoColorTheme(t *testing.T) {
	theme := NoColorTheme()

	assert.Empty(t, theme.Primary.Light)
	assert.Empty(t, theme.Primary.Dark)
	assert.Empty(t, theme.Error.Light)
	assert.Empty(t, theme.Error.Dark)
	assert.Empty(t, theme.Success.Light)
	assert.Empty(t, theme.Success.Dark)
	assert.Empty(t, theme.Foreground.Light)
	assert.Empty(t, theme.Foreground.Dark)
}

func unsetenvForTest(t *testing.T, key string) {
	t.Helper()
	prev, existed := os.LookupEnv(key)
	os.Unsetenv(key)
	if existed {
		t.Cleanup(func() { os.Setenv(key, prev) })
	}
}

func TestResolveTheme(t *testing.T) {
	t.Run("NO_COLOR returns empty theme", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")

		theme := ResolveTheme()

		assert.Empty(t, theme.Primary.Light)
		assert.Empty(t, theme.Primary.Dark)
	})

	t.Run("BASECAMP_THEME loads custom file", func(t *testing.T) {
		unsetenvForTest(t, "NO_COLOR")

		tmpDir := t.TempDir()
		testFile := filepath.Join(tmpDir, "custom.toml")

		content := `accent = "#ff0000"
foreground = "#ffffff"
`
		err := os.WriteFile(testFile, []byte(content), 0644)
		require.NoError(t, err, "Failed to write test file")

		t.Setenv("BASECAMP_THEME", testFile)

		theme := ResolveTheme()

		assert.Equal(t, "#ff0000", theme.Primary.Dark)
	})

	t.Run("BASECAMP_THEME invalid file falls back", func(t *testing.T) {
		unsetenvForTest(t, "NO_COLOR")
		t.Setenv("BASECAMP_THEME", "/nonexistent/theme.toml")

		theme := ResolveTheme()

		// Should fall back to Omarchy or default - just check it's not empty
		assert.False(t, theme.Primary.Dark == "" && theme.Primary.Light == "",
			"With invalid BASECAMP_THEME, should fall back to a valid theme")
	})

	t.Run("default theme when no env vars", func(t *testing.T) {
		unsetenvForTest(t, "NO_COLOR")
		unsetenvForTest(t, "BASECAMP_THEME")

		theme := ResolveTheme()

		// Should return a valid theme (either user theme or default)
		// We just verify it's not empty - could be user config or default
		assert.False(t, theme.Primary.Dark == "" && theme.Primary.Light == "",
			"ResolveTheme() returned theme with empty Primary color")
	})
}

func TestLoadUserTheme(t *testing.T) {
	t.Run("loads theme from user config dir", func(t *testing.T) {
		unsetenvForTest(t, "NO_COLOR")

		// Create a temporary home directory structure
		tmpHome := t.TempDir()
		themeDir := filepath.Join(tmpHome, ".config", "basecamp", "theme")
		err := os.MkdirAll(themeDir, 0755)
		require.NoError(t, err, "Failed to create theme dir")

		content := `accent = "#00ff00"
foreground = "#eeeeee"
`
		themeFile := filepath.Join(themeDir, "colors.toml")
		err = os.WriteFile(themeFile, []byte(content), 0644)
		require.NoError(t, err, "Failed to write theme file")

		// LoadUserTheme uses os.UserHomeDir, so we test via BASECAMP_THEME instead
		// since we can't easily mock the home directory
		t.Setenv("BASECAMP_THEME", themeFile)
		theme := ResolveTheme()

		assert.Equal(t, "#00ff00", theme.Primary.Dark)
	})

	t.Run("returns error for missing config", func(t *testing.T) {
		_, err := LoadThemeFromFile("/nonexistent/path/colors.toml")
		assert.Error(t, err, "LoadThemeFromFile should return error for missing file")
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
		assert.Equal(t, tt.want, got)
	}
}
