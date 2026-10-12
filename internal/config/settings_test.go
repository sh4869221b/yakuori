package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettings(t *testing.T) {
	t.Run("schema one round trips selected model", func(t *testing.T) {
		want := Settings{SelectedModel: "index-translate"}
		encoded, err := EncodeSettings(want)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != `{"schema":1,"selected_model":"index-translate"}` {
			t.Fatalf("encoded settings = %s", encoded)
		}
		got, err := DecodeSettings(encoded)
		if err != nil || got != want {
			t.Fatalf("decoded settings = %+v, error = %v; want %+v", got, err, want)
		}
	})

	t.Run("rejects invalid settings documents", func(t *testing.T) {
		for _, tt := range []struct {
			name string
			data string
		}{
			{"unknown schema", `{"schema":2,"selected_model":"one"}`},
			{"schema has wrong type", `{"schema":"1","selected_model":"one"}`},
			{"selected model has wrong type", `{"schema":1,"selected_model":3}`},
			{"selected model is missing", `{"schema":1}`},
			{"unknown field", `{"schema":1,"selected_model":"one","extra":true}`},
			{"trailing json", `{"schema":1,"selected_model":"one"}{}`},
			{"invalid model id", `{"schema":1,"selected_model":"../one"}`},
		} {
			t.Run(tt.name, func(t *testing.T) {
				if _, err := DecodeSettings([]byte(tt.data)); err == nil {
					t.Fatalf("DecodeSettings(%s) succeeded", tt.data)
				}
			})
		}
	})

	t.Run("validates model ids for path use", func(t *testing.T) {
		for _, tt := range []struct {
			id      string
			invalid bool
		}{
			{"A", false},
			{"9_x-9", false},
			{strings.Repeat("a", 64), false},
			{"", true},
			{"-one", true},
			{"_one", true},
			{"../one", true},
			{"日本語", true},
			{strings.Repeat("a", 65), true},
		} {
			t.Run(tt.id, func(t *testing.T) {
				err := ValidateModelID(tt.id)
				if tt.invalid && !errors.Is(err, ErrInvalidModelID) {
					t.Fatalf("ValidateModelID(%q) error = %v; want %v", tt.id, err, ErrInvalidModelID)
				}
				if !tt.invalid && err != nil {
					t.Fatalf("ValidateModelID(%q): %v", tt.id, err)
				}
			})
		}
	})

	t.Run("missing settings are unselected and corruption is an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		got, err := LoadSettings(path)
		if err != nil || got != (Settings{}) {
			t.Fatalf("LoadSettings(missing) = %+v, error = %v", got, err)
		}
		if err := os.WriteFile(path, []byte(`{"schema":2,"selected_model":"one"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSettings(path); err == nil {
			t.Fatal("LoadSettings(corrupt) succeeded")
		}
	})

	t.Run("encoding rejects invalid model ids", func(t *testing.T) {
		if _, err := EncodeSettings(Settings{SelectedModel: "../one"}); !errors.Is(err, ErrInvalidModelID) {
			t.Fatalf("EncodeSettings invalid model error = %v", err)
		}
	})
}
