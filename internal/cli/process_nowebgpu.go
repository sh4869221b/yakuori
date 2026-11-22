//go:build !gpu || !cgo || !linux || !amd64

package cli

func PrepareProcess([]string) error { return nil }
