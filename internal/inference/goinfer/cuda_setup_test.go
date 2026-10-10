//go:build cuda && cudasmoke

package goinfer

import (
	"testing"
)

func TestCUDAEvaluationContext(t *testing.T) {
	for _, c := range []struct {
		name      string
		setup     cudaSetup
		cap       int
		supported bool
	}{
		{"cpu zero resident cap", cudaSetup{RequestedBackend: "cpu", EffectiveBackend: "cpu", Quant: "int4", ModelContext: 262144}, 4096, true},
		{"resident cap", cudaSetup{RequestedBackend: "cuda", EffectiveBackend: "cuda", Quant: "int4", ModelContext: 262144, ResidentActive: true, ResidentCap: 2048}, 2048, true},
		{"declined", cudaSetup{RequestedBackend: "cuda", EffectiveBackend: "cuda", Quant: "int4", ModelContext: 262144, ResidentDecline: "test decline"}, 4096, false},
		{"unknown resident cap", cudaSetup{RequestedBackend: "cuda", EffectiveBackend: "cuda", Quant: "int4", ModelContext: 262144, ResidentActive: true}, 4096, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cap, err := cudaContext(c.setup)
			if cap != c.cap || (err == nil) != c.supported {
				t.Fatalf("cap=%d err=%v", cap, err)
			}
		})
	}
}
