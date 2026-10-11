package goinfer

import (
	"strconv"
	"strings"

	"github.com/sh4869221b/yakuori/internal/inference"
)

func prefillPolicy(model modelBackend, backend string) inference.PrefillPolicy {
	knob := func(name string) string { value, _ := model.Knob(name); return value }
	p := inference.PrefillPolicy{Exact: model.ExactPrefill(), Path: backend, Fast: "off"}
	switch backend {
	case "cpu":
		p.CPUFastAttention = !p.Exact && knob("GOINFER_CPU_FAST_ATTENTION") != "0"
	case "cuda":
		p.Batched = knob("GOINFER_BATCHED_PREFILL") != "0"
		p.BatchedCapability, _ = model.PrefillPath()
		if !p.Batched || !p.BatchedCapability {
			return p
		}
		p.Chunk, _ = strconv.Atoi(knob("GOINFER_PREFILL_CHUNK"))
		if p.Chunk <= 0 {
			p.Chunk = 512
		}
		if p.Exact {
			return p
		}
		switch strings.ToLower(strings.TrimSpace(knob("GOINFER_CUDA_FAST_PREFILL"))) {
		case "0", "false", "off":
		case "attn":
			p.Fast = "attn"
		case "gemm":
			p.Fast = "gemm"
		default:
			p.Fast = "both"
		}
		if p.Fast != "off" {
			floor, err := strconv.Atoi(knob("GOINFER_CUDA_FAST_PREFILL_FLOOR"))
			if err != nil || floor < 0 {
				floor = 512
			}
			p.Floor = floor
		}
	}
	return p
}
