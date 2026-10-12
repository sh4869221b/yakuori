package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestStoragePaths(t *testing.T) {
	home := t.TempDir()
	paths, err := Resolve(func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}

	got := ResolveStoragePaths(paths)
	want := StoragePaths{
		ConfigFile:   filepath.Join(home, ".config/yakuori/config.json"),
		RegistryFile: filepath.Join(home, ".local/share/yakuori/registry.json"),
		ModelsDir:    filepath.Join(home, ".local/share/yakuori/models"),
		TMFile:       filepath.Join(home, ".local/share/yakuori/tm.sqlite"),
		SnapshotDir:  filepath.Join(home, ".cache/yakuori/model-snapshots"),
		RegistryLock: filepath.Join(home, ".local/share/yakuori/registry.lock"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage paths = %#v, want %#v", got, want)
	}

	settings, err := LoadSettings(got.ConfigFile)
	if err != nil || settings != (Settings{}) {
		t.Fatalf("missing settings = %+v, error = %v; want unselected", settings, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("resolving and loading created files under HOME: %v", entries)
	}
}
