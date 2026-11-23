//go:build cuda && linux && amd64

package goinfer

import (
	"fmt"

	driver "github.com/eitamring/gocudrv/cuda"
	_ "github.com/townsendmerino/goinfer/backend/cuda"
	"github.com/townsendmerino/goinfer/decoder"
)

const cudaBackendPin = "github.com/sh4869221b/goinfer/backend/cuda@688d8d9a1d13efda8356277f7e7f78e03807d9b3"

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
