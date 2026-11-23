//go:build cuda && linux && amd64

package goinfer

import (
	"fmt"

	driver "github.com/eitamring/gocudrv/cuda"
	_ "github.com/townsendmerino/goinfer/backend/cuda"
	"github.com/townsendmerino/goinfer/decoder"
)

const cudaBackendPin = "github.com/sh4869221b/goinfer/backend/cuda@bb3ca6430d39cc4850d85cb2981d1f069115ea9d"

func loadDecoder(path string, options decoder.Options) (*decoder.Model, error) {
	if options.Backend == "cuda" {
		// Reject driver initialization errors before upstream residency prints
		// them as CPU continuation. Later resident/device failures remain upstream.
		if err := driver.Init(); err != nil {
			return nil, fmt.Errorf("%w: initialize CUDA driver: %w", ErrCUDAResidency, err)
		}
	}
	return decoder.Load(path, options)
}
