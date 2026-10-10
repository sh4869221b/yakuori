//go:build cuda && linux && amd64

package goinfer

import _ "github.com/townsendmerino/goinfer/cuda"

const cudaBackendPin = "github.com/townsendmerino/goinfer/cuda@v0.22.0"
