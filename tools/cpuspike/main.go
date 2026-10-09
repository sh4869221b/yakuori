// Command cpuspike is an explicit, model-required feasibility probe for issue #3.
// It is not the production Engine, a translator, or a model downloader.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"time"

	"github.com/townsendmerino/goinfer/chat"
	"github.com/townsendmerino/goinfer/decoder"
	"github.com/townsendmerino/goinfer/tokenizer"
)

const modelDigest = "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32"
const modelBytes int64 = 491400064

type observation struct {
	Name         string `json:"name"`
	Finish       string `json:"finish"`
	Tokens       []int  `json:"tokens"`
	Text         string `json:"text"`
	Error        string `json:"error,omitempty"`
	Milliseconds int64  `json:"milliseconds"`
	Budget       int    `json:"backend_budget"`
	Clamped      bool   `json:"backend_budget_clamped"`
}
type report struct {
	Go           string        `json:"go"`
	Platform     string        `json:"platform"`
	Digest       string        `json:"model_sha256"`
	Template     string        `json:"template"`
	Rendered     string        `json:"rendered_request"`
	PromptTokens []int         `json:"prompt_token_ids"`
	Context      int           `json:"model_context"`
	Quant        string        `json:"compute_quant"`
	LoadMS       int64         `json:"load_ms"`
	Cases        []observation `json:"cases"`
}

// Classification is deliberately limited to the pinned plain CPU Generate path.
// Err is read only after drain. Context wins over a simultaneous nil backend Err.
func finish(err, ctxErr error, emitted, requested, budget int, clamped bool) string {
	if errors.Is(ctxErr, context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "Timeout"
	}
	if errors.Is(ctxErr, context.Canceled) || errors.Is(err, context.Canceled) {
		return "Canceled"
	}
	if err != nil {
		return "DecodeError"
	}
	if requested <= 0 || emitted < 0 || emitted > requested {
		return "InvalidOutput"
	}
	if clamped {
		return "ContextLimit"
	} // never accept a silently shortened request
	if budget != requested {
		return "InvalidOutput"
	}
	if emitted == requested {
		return "MaxTokens"
	}
	return "Stop" // only remaining normal exits in pinned CPU loop are EOS/StopIDs
}

func withinContext(prompt, output, limit int) bool {
	return prompt > 0 && output > 0 && limit > 0 && prompt <= limit && output <= limit-prompt
}

func verifyModel(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != modelBytes {
		return fmt.Errorf("expected regular %d-byte fixture", modelBytes)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != modelDigest {
		return errors.New("fixture SHA-256 mismatch")
	}
	return nil
}

func observe(m *decoder.Model, tok *tokenizer.Tokenizer, ctx context.Context, ids []int, max int, sp decoder.SamplingParams, name string, afterFirst func()) observation {
	start := time.Now()
	ch, g := m.Generate(ctx, ids, max, sp)
	o := observation{Name: name, Tokens: []int{}}
	for id := range ch {
		o.Tokens = append(o.Tokens, id)
		if len(o.Tokens) == 1 && afterFirst != nil {
			afterFirst()
		}
	}
	o.Milliseconds = time.Since(start).Milliseconds()
	o.Budget = g.Budget
	o.Clamped = g.BudgetClamped
	err := g.Err()
	o.Finish = finish(err, ctx.Err(), len(o.Tokens), max, g.Budget, g.BudgetClamped)
	if err != nil {
		o.Error = err.Error()
	}
	text, err := tok.Decode(o.Tokens)
	if err != nil {
		o.Finish = "DecodeError"
		o.Error = err.Error()
	} else {
		o.Text = text
	}
	return o
}

func probe(path string) (r report, err error) {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "CGO_ENABLED" && setting.Value != "0" {
				return r, errors.New("probe must be built with CGO_ENABLED=0")
			}
		}
	}
	r.Go = runtime.Version()
	r.Platform = runtime.GOOS + "/" + runtime.GOARCH
	r.Digest = modelDigest
	if runtime.GOOS != "linux" {
		return r, errors.New("Linux-only spike")
	}
	if err = verifyModel(path); err != nil {
		return r, err
	}
	start := time.Now()
	m, err := decoder.Load(path, decoder.Options{Backend: "cpu", Quant: "int4"})
	if err != nil {
		return r, err
	}
	defer m.Close()
	r.LoadMS = time.Since(start).Milliseconds()
	r.Context = m.Config().MaxPositions
	r.Quant = m.Quant()
	if r.Context != 32768 || r.Quant != "int4" {
		return r, fmt.Errorf("unexpected fixture context/quant: %d/%s", r.Context, r.Quant)
	}
	tok, err := tokenizer.LoadGGUF(path)
	if err != nil {
		return r, err
	}
	if d := tok.PreTokenizerDecline(); d != "" {
		return r, fmt.Errorf("tokenizer declined: %s", d)
	}
	tmpl, err := chat.Detect(chat.Meta{ChatTemplate: tok.ChatTemplate(), HasToken: tok.Has})
	if err != nil {
		return r, err
	}
	r.Template = tmpl.Name()
	r.Rendered = tmpl.Render("You are a helpful assistant.", []chat.Turn{{Role: "user", Content: "Reply with only the word Hello."}})
	ids, err := tok.Encode(r.Rendered, false)
	if err != nil {
		return r, err
	}
	r.PromptTokens = slices.Clone(ids)
	// Frozen observed token IDs for this exact model + tokenizer pin + rendered fixture.
	golden := []int{151644, 8948, 198, 2610, 525, 264, 10950, 17847, 13, 151645, 198, 151644, 872, 198, 20841, 448, 1172, 279, 3409, 21927, 13, 151645, 198, 151644, 77091, 198}
	if !slices.Equal(ids, golden) {
		return r, errors.New("pinned prompt token golden mismatch")
	}
	counted, err := tok.Encode(r.Rendered, false)
	if err != nil {
		return r, err
	}
	if !slices.Equal(ids, counted) || !withinContext(len(ids), 128, r.Context) {
		return r, errors.New("same rendered request token/budget mismatch")
	}
	// Reject out-of-window requests BEFORE backend invocation; do not allocate/prefill 32K tokens.
	if withinContext(r.Context, 1, r.Context) || !withinContext(r.Context-1, 1, r.Context) {
		return r, errors.New("context boundary gate failed")
	}
	var stops []int
	for _, s := range tmpl.Stops().Strings {
		id, ok := tok.TokenID(s)
		if !ok {
			return r, fmt.Errorf("unknown stop %q", s)
		}
		stops = append(stops, id)
	}
	if len(stops) == 0 {
		return r, errors.New("missing template stop IDs")
	}
	sp := decoder.SamplingParams{StopIDs: stops}
	// Every real generation has a finite probe deadline. These are test limits, not product defaults.
	run := func(name string, max int, params decoder.SamplingParams) observation {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return observe(m, tok, ctx, ids, max, params, name, nil)
	}
	natural := run("natural_cpu_generation", 128, sp)
	r.Cases = append(r.Cases, natural)
	if natural.Finish != "Stop" || len(natural.Tokens) == 0 {
		return r, fmt.Errorf("natural generation did not stop successfully: %+v", natural)
	}
	// Controlled logit fixtures still execute real CPU forward passes. They isolate terminal semantics,
	// not language quality: emit a real non-stop token, then the template stop ID.
	token := natural.Tokens[0]
	force := func(stopAfter int) decoder.SamplingParams {
		p := sp
		p.LogitProcessor = func(generated []int, logits []float32) {
			id := token
			if len(generated) >= stopAfter {
				id = stops[0]
			}
			for i := range logits {
				logits[i] = float32(math.Inf(-1))
			}
			logits[id] = 0
		}
		return p
	}
	for _, tc := range []struct {
		name      string
		max, stop int
		want      string
	}{{"forced_stop", 8, 1, "Stop"}, {"max_tokens", 1, 8, "MaxTokens"}} {
		o := run(tc.name, tc.max, force(tc.stop))
		r.Cases = append(r.Cases, o)
		if o.Finish != tc.want || len(o.Tokens) != 1 {
			return r, fmt.Errorf("%s failed: %+v", tc.name, o)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	cctx, ccancel := context.WithCancel(ctx)
	o := observe(m, tok, cctx, ids, 32, force(32), "cancel_after_partial", ccancel)
	ccancel()
	cancel()
	r.Cases = append(r.Cases, o)
	if o.Finish != "Canceled" || len(o.Tokens) == 0 || o.Error == "" {
		return r, fmt.Errorf("partial cancellation failed: %+v", o)
	}
	// Deadline fires while a real prefill is underway, not only before Generate.
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	o = observe(m, tok, ctx, ids, 32, sp, "deadline_during_generation", nil)
	cancel()
	r.Cases = append(r.Cases, o)
	if o.Finish != "Timeout" || o.Error == "" {
		return r, fmt.Errorf("deadline failed: %+v", o)
	}
	// A subsequent healthy request must work; canceled streams have been drained before reuse.
	o = run("healthy_after_cancel_and_deadline", 8, force(1))
	r.Cases = append(r.Cases, o)
	if o.Finish != "Stop" {
		return r, fmt.Errorf("reuse failed: %+v", o)
	}
	return r, nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "long" {
		os.Exit(longMain(os.Args[2:]))
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: cpuspike /absolute/path/to/pinned-model.gguf")
		os.Exit(2)
	}
	r, err := probe(os.Args[1])
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if e := enc.Encode(r); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
