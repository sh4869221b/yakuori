//go:build !cuda || !linux || !amd64

package goinfer

import "github.com/townsendmerino/goinfer/decoder"

const cudaBackendPin = ""

func loadDecoder(path string, options decoder.Options) (*decoder.Model, error) {
	return decoder.Load(path, options)
}
