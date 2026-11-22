//go:build !gpu || !cgo || !linux || !amd64

package goinfer

const webGPUBackendPin = ""

func webGPUAdapter() (string, string, error) {
	return "", "", ErrUnsupportedBackend
}
