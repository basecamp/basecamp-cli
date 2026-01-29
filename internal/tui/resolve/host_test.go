package resolve

import (
	"context"
	"testing"

	"github.com/basecamp/bcq/internal/config"
)

func TestHostResolution(t *testing.T) {
	tests := []struct {
		name       string
		flags      *Flags
		config     *config.Config
		envHost    string
		wantURL    string
		wantSource ResolvedSource
		wantErr    bool
	}{
		{
			name:  "flag takes precedence",
			flags: &Flags{Host: "https://staging.example.com"},
			config: &config.Config{
				BaseURL: "https://production.example.com",
				Hosts: map[string]*config.HostConfig{
					"production": {BaseURL: "https://production.example.com"},
				},
			},
			wantURL:    "https://staging.example.com",
			wantSource: SourceFlag,
		},
		{
			name:    "env var over config",
			flags:   &Flags{},
			envHost: "https://env.example.com",
			config: &config.Config{
				BaseURL:     "https://production.example.com",
				DefaultHost: "production",
				Hosts: map[string]*config.HostConfig{
					"production": {BaseURL: "https://production.example.com"},
				},
			},
			wantURL:    "https://env.example.com",
			wantSource: SourceConfig,
		},
		{
			name:  "default_host from config",
			flags: &Flags{},
			config: &config.Config{
				DefaultHost: "staging",
				Hosts: map[string]*config.HostConfig{
					"production": {BaseURL: "https://production.example.com"},
					"staging":    {BaseURL: "https://staging.example.com"},
				},
			},
			wantURL:    "https://staging.example.com",
			wantSource: SourceConfig,
		},
		{
			name:  "single host auto-selected",
			flags: &Flags{},
			config: &config.Config{
				Hosts: map[string]*config.HostConfig{
					"production": {BaseURL: "https://only.example.com"},
				},
			},
			wantURL:    "https://only.example.com",
			wantSource: SourceDefault,
		},
		{
			name:  "falls back to base_url",
			flags: &Flags{},
			config: &config.Config{
				BaseURL: "https://fallback.example.com",
			},
			wantURL:    "https://fallback.example.com",
			wantSource: SourceConfig,
		},
		{
			name:       "default production URL",
			flags:      &Flags{},
			config:     &config.Config{},
			wantURL:    "https://3.basecampapi.com",
			wantSource: SourceDefault,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envHost != "" {
				t.Setenv("BCQ_HOST", tt.envHost)
			}

			r := New(nil, nil, tt.config, WithFlags(tt.flags))

			got, err := r.Host(context.Background())
			if (err != nil) != tt.wantErr {
				t.Fatalf("Host() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}

			if got.Value != tt.wantURL {
				t.Errorf("Host() URL = %q, want %q", got.Value, tt.wantURL)
			}
			if got.Source != tt.wantSource {
				t.Errorf("Host() Source = %v, want %v", got.Source, tt.wantSource)
			}
		})
	}
}
