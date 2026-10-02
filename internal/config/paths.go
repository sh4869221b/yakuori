// Package config defines the initial, read-only Yakuori storage contract.
// File formats, model registry and TM are intentionally not implemented yet.
package config

import (
	"fmt"
	"path/filepath"
)

// Paths are absolute, Yakuori-only directories. Resolving them creates nothing.
type Paths struct{ Config, Data, Cache, State string }

// Resolve follows XDG Base Directory defaults. Relative XDG values are ignored.
// HOME is required only when a default must be used. No Kotoba paths are read.
func Resolve(getenv func(string) string) (Paths, error) {
	var p Paths
	for _, entry := range []struct {
		key, fallback string
		target        *string
	}{
		{"XDG_CONFIG_HOME", ".config", &p.Config},
		{"XDG_DATA_HOME", ".local/share", &p.Data},
		{"XDG_CACHE_HOME", ".cache", &p.Cache},
		{"XDG_STATE_HOME", ".local/state", &p.State},
	} {
		base := getenv(entry.key)
		if !filepath.IsAbs(base) {
			home := getenv("HOME")
			if !filepath.IsAbs(home) {
				return Paths{}, fmt.Errorf("%s needs an absolute value or absolute HOME", entry.key)
			}
			base = filepath.Join(home, entry.fallback)
		}
		*entry.target = filepath.Join(base, "yakuori")
	}
	return p, nil
}
