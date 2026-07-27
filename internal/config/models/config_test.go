package modelsconfig

// Verifies complete models-resource validation before resource identity hashing.

import (
	"strings"
	"testing"
)

func TestNormalizeValidatesCompleteModelsResource(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*Config)
		wantErr string
	}{
		{
			name: "upstream query",
			change: func(config *Config) {
				config.URL = "https://example.invalid/v1?secret=value"
			},
			wantErr: "query",
		},
		{
			name: "reserved header",
			change: func(config *Config) {
				config.Headers = map[string]string{"Host": "elsewhere"}
			},
			wantErr: "reserved",
		},
		{
			name: "invalid display name",
			change: func(config *Config) {
				config.Name = " padded "
			},
			wantErr: "display name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := Config{
				Protocol: ProtocolOpenAI,
				URL:      "https://example.invalid/v1",
			}
			test.change(&config)

			_, err := Normalize(config)
			if err == nil ||
				!strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf(
					"Normalize() error = %v, want containing %q",
					err,
					test.wantErr,
				)
			}
		})
	}
}
