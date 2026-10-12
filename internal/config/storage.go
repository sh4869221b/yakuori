package config

import "path/filepath"

// StoragePaths names the files and directories used by Yakuori storage.
type StoragePaths struct {
	ConfigFile   string
	RegistryFile string
	ModelsDir    string
	TMFile       string
	SnapshotDir  string
	RegistryLock string
}

// ResolveStoragePaths derives concrete storage paths without creating them.
func ResolveStoragePaths(paths Paths) StoragePaths {
	return StoragePaths{
		ConfigFile:   filepath.Join(paths.Config, "config.json"),
		RegistryFile: filepath.Join(paths.Data, "registry.json"),
		ModelsDir:    filepath.Join(paths.Data, "models"),
		TMFile:       filepath.Join(paths.Data, "tm.sqlite"),
		SnapshotDir:  filepath.Join(paths.Cache, "model-snapshots"),
		RegistryLock: filepath.Join(paths.Data, "registry.lock"),
	}
}
