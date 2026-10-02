package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/townsendmerino/goinfer/chat"
	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"
)

// longProbe is an opt-in synthetic capacity experiment, never a translation gate.
func longProbe(path string, target int, mode string) error {
	if target < 32 || target > 32766 || (mode != "stop" && mode != "deadline") {
		return fmt.Errorf("target must be 32..32766; mode stop or deadline")
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "CGO_ENABLED" && setting.Value != "0" {
				return fmt.Errorf("probe must be built with CGO_ENABLED=0")
			}
		}
	}
	if runtime.GOOS != "linux" {
		return fmt.Errorf("Linux-only experiment")
	}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "GOINFER_") {
			return fmt.Errorf("remove ambient GOINFER tuning: %s", strings.SplitN(entry, "=", 2)[0])
		}
	}
	if err := verifyModel(path); err != nil {
		return err
	}
	start := time.Now()
	m, err := decoder.Load(path, decoder.Options{Backend: "cpu", Quant: "int4"})
	if err != nil {
		return err
	}
	defer m.Close()
	if m.Config().MaxPositions != 32768 || m.Quant() != "int4" {
		return fmt.Errorf("unexpected fixture context/quant")
	}
	load := time.Since(start)
	tok, err := tokenizer.LoadGGUF(path)
	if err != nil {
		return err
	}
	if tok.PreTokenizerDecline() != "" {
		return fmt.Errorf("tokenizer declined")
	}
	tmpl, err := chat.Detect(chat.Meta{ChatTemplate: tok.ChatTemplate(), HasToken: tok.Has})
	if err != nil {
		return err
	}
	// Repeated ordinary text is a synthetic workload, not a quality benchmark.
	// Count the complete rendered text. Never truncate token IDs or cut the template.
	render := func(n int) string {
		return tmpl.Render("You are a helpful assistant.", []chat.Turn{{Role: "user", Content: strings.Repeat(" hello", n) + "\nReply with only Hello."}})
	}
	base, err := tok.Encode(render(1), false)
	if err != nil {
		return err
	}
	if target < len(base) {
		return fmt.Errorf("target below template size")
	}
	rendered := render(target - len(base) + 1)
	ids, err := tok.Encode(rendered, false)
	if err != nil {
		return err
	}
	again, err := tok.Encode(rendered, false)
	if err != nil {
		return err
	}
	if len(ids) != target || !slices.Equal(ids, again) || !withinContext(len(ids), 2, m.Config().MaxPositions) {
		return fmt.Errorf("exact token/admission mismatch: got %d want %d", len(ids), target)
	}
	if len(tmpl.Stops().Strings) == 0 {
		return fmt.Errorf("missing template stops")
	}
	stop, ok := tok.TokenID(tmpl.Stops().Strings[0])
	if !ok {
		return fmt.Errorf("missing stop")
	}
	hello, ok := tok.TokenID("Hello")
	if !ok {
		return fmt.Errorf("missing Hello")
	}
	sp := decoder.SamplingParams{StopIDs: []int{stop}, LogitProcessor: func(generated []int, logits []float32) {
		id := hello
		if len(generated) > 0 {
			id = stop
		}
		for i := range logits {
			logits[i] = float32(math.Inf(-1))
		}
		logits[id] = 0
	}}
	limit := 5 * time.Minute
	if mode == "deadline" {
		limit = 100 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	genStart := time.Now()
	o := observe(m, tok, ctx, ids, 2, sp, "long_"+mode, nil)
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return err
	}
	encoded, _ := json.Marshal(ids)
	record := map[string]any{"go": runtime.Version(), "platform": runtime.GOOS + "/" + runtime.GOARCH, "gomaxprocs": runtime.GOMAXPROCS(0), "model_sha256": modelDigest, "model_shape": map[string]int{"layers": m.Config().NumLayers, "hidden": m.Config().HiddenDim, "heads": m.Config().NumHeads, "kv_heads": m.Config().NumKVHeads, "head_dim_derived": m.Config().HiddenDim / m.Config().NumHeads, "intermediate": m.Config().IntermediateDim, "max_positions": m.Config().MaxPositions}, "compute_quant": m.Quant(), "template": tmpl.Name(), "prompt_tokens": len(ids), "prompt_token_ids_sha256": fmt.Sprintf("%x", sha256.Sum256(encoded)), "rendered_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(rendered))), "load_ms": load.Milliseconds(), "peak_rss_kib": usage.Maxrss, "request_deadline_ms": limit.Milliseconds(), "observation": o, "deadline_overrun_ms": max(int64(0), time.Since(genStart).Milliseconds()-limit.Milliseconds()), "cpu_fast_attention": "upstream default enabled", "fused_attention": "upstream default enabled"}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(record); err != nil {
		return err
	}
	if mode == "stop" && (o.Finish != "Stop" || len(o.Tokens) != 1 || o.Text != "Hello") {
		return fmt.Errorf("long stop failed: %+v", o)
	}
	if mode == "deadline" && (o.Finish != "Timeout" || len(o.Tokens) != 0 || o.Error == "") {
		return fmt.Errorf("long deadline failed: %+v", o)
	}
	return nil
}

func longMain(args []string) int {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "long mode: model target-tokens stop|deadline")
		return 2
	}
	n, err := strconv.Atoi(args[1])
	if err == nil {
		err = longProbe(args[0], n, args[2])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
