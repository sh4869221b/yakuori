//go:build gpu && cgo && linux && amd64

package cli

import (
	"strings"
	"testing"
)

func TestUsesWebGPU(t *testing.T) {
	const scope = " --source-language en --target-language ja --game game --artifact asset"
	for _, tt := range []struct {
		args string
		want bool
	}{
		{"translate" + scope + " --backend=webgpu source", true},
		{"localize" + scope + " --format w3strings --backend webgpu --output translated source", true},
		{"doctor --backend webgpu --check-model", true},
		{"doctor --backend webgpu", false},
		{"doctor --check-model --backend cuda", false},
		{"translate --backend webgpu --help", false},
		{"translate" + scope + " --backend webgpu --backend cpu source", false},
		{"translate" + scope + " -- --backend=webgpu", false},
		{"models use webgpu", false},
		{"context build" + scope + " --infer --backend webgpu --output-dir out source", true},
		{"context build" + scope + " --backend webgpu --output-dir out source", false},
		{"context build" + scope + " --infer --backend cpu --output-dir out source", false},
		{"context build" + scope + " --infer --backend webgpu source", false},
		{"context build --infer --backend webgpu --help", false},
	} {
		if got := usesWebGPU(strings.Fields(tt.args)); got != tt.want {
			t.Errorf("usesWebGPU(%q) = %t, want %t", tt.args, got, tt.want)
		}
	}
}
