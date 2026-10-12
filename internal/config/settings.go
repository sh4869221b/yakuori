package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const settingsSchema = 1

var ErrInvalidModelID = errors.New("invalid model ID")

// Settings contains the user's selected model. An absent config file means no selection.
type Settings struct {
	SelectedModel string `json:"selected_model"`
}

type settingsDocument struct {
	Schema        int    `json:"schema"`
	SelectedModel string `json:"selected_model"`
}

// ValidateModelID accepts safe ASCII model IDs that can be used as path elements.
func ValidateModelID(id string) error {
	if len(id) == 0 || len(id) > 64 || !asciiAlphaNumeric(id[0]) {
		return ErrInvalidModelID
	}
	for i := 1; i < len(id); i++ {
		c := id[i]
		if !asciiAlphaNumeric(c) && c != '-' && c != '_' {
			return ErrInvalidModelID
		}
	}
	return nil
}

func asciiAlphaNumeric(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

// DecodeSettings parses a schema 1 config and rejects unknown fields and trailing data.
func DecodeSettings(data []byte) (Settings, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document settingsDocument
	if err := decoder.Decode(&document); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return Settings{}, errors.New("decode settings: trailing data")
		}
		return Settings{}, fmt.Errorf("decode settings: trailing data: %w", err)
	}
	if document.Schema != settingsSchema {
		return Settings{}, fmt.Errorf("unsupported settings schema: %d", document.Schema)
	}
	if err := ValidateModelID(document.SelectedModel); err != nil {
		return Settings{}, fmt.Errorf("selected model: %w", err)
	}
	return Settings{SelectedModel: document.SelectedModel}, nil
}

// EncodeSettings serializes a selected model using the current config schema.
func EncodeSettings(settings Settings) ([]byte, error) {
	if err := ValidateModelID(settings.SelectedModel); err != nil {
		return nil, fmt.Errorf("selected model: %w", err)
	}
	return json.Marshal(settingsDocument{Schema: settingsSchema, SelectedModel: settings.SelectedModel})
}

// LoadSettings reads settings from path. A missing file means no model is selected.
func LoadSettings(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read settings: %w", err)
	}
	return DecodeSettings(data)
}
