package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"unicode/utf8"

	textadapter "github.com/sh4869221b/yakuori/internal/adapter/text"
	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/identity"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/tm"
	"github.com/sh4869221b/yakuori/internal/unit"
	"golang.org/x/sys/unix"
)

type selectedEngineOpener func(context.Context, string) (string, inference.Engine, error)

type requestFactory interface {
	NewRequest(context.Context, prompt.Input, inference.GenerationPolicy) (inference.GenerationRequest, error)
}

var errTranslationInput = errors.New("translation requires a bounded regular UTF-8 file")
var errRequestFactory = errors.New("selected engine cannot construct requests")

// The opener is the only test seam; Store and Core are always the real implementations.
// Context, output, resolved storage and the selected-model loader have independent lifetimes.
func (command translationCommand) execute(ctx context.Context, stdout io.Writer, paths config.Paths, open selectedEngineOpener) (result localize.Result, err error) {
	var adapter localize.Adapter = textadapter.New(command.sourceLanguage, command.targetLanguage)
	if command.format == "w3strings" {
		table, err := w3strings.NewForSource(command.sourceLanguage, config.DefaultLimits())
		if err != nil {
			return result, &localize.Error{Phase: "import", Err: err}
		}
		adapter = table
	}
	id, engine, err := open(ctx, command.backend)
	if err != nil {
		return result, &localize.Error{Phase: "model", Err: err}
	}
	defer func() {
		if closeErr := engine.Close(); closeErr != nil {
			err = errors.Join(err, &localize.Error{Phase: "close", Err: closeErr})
		}
	}()
	factory, ok := engine.(requestFactory)
	if !ok {
		return result, &localize.Error{Phase: "profile", Err: errRequestFactory}
	}
	info, err := engine.Info(ctx)
	if err != nil {
		return result, &localize.Error{Phase: "profile", Err: err}
	}
	limits := config.DefaultLimits()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, limits.MaxOutputTokens, limits.RequestTimeout)
	if err != nil {
		return result, &localize.Error{Phase: "profile", Err: err}
	}
	build := func(ctx context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		return factory.NewRequest(ctx, prompt.Input{SourceLanguage: command.sourceLanguage, TargetLanguage: command.targetLanguage, Text: text}, policy)
	}
	request, err := build(ctx, unit.UnitID{}, "")
	if err != nil {
		return result, &localize.Error{Phase: "profile", Err: err}
	}
	profile, err := localize.NewCanonicalInferenceProfile(info, request, localize.InferenceProfileInput{
		ModelID: id, Glossary: identity.GlossaryInput{Mode: "none"}, Limits: limits,
	})
	if err != nil {
		return result, &localize.Error{Phase: "profile", Err: err}
	}
	path := config.ResolveStoragePaths(paths).TMFile
	var store *tm.Store
	_, statErr := os.Lstat(path)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		store, err = tm.Create(ctx, path, limits)
	case statErr != nil:
		err = statErr
	default:
		store, err = tm.Open(ctx, path, limits)
	}
	if err != nil {
		return result, &localize.Error{Phase: "tm_open", Err: err}
	}
	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			err = errors.Join(err, &localize.Error{Phase: "close", Err: closeErr})
		}
	}()
	core := localize.NewCore(localize.NewInferenceEngine(engine, build))
	if command.action == "localize" {
		return core.FileWithTM(ctx, command.options, adapter, profile, store, command.scope)
	}
	input, err := readTranslationInput(command.options.Source, limits)
	if err != nil {
		return result, &localize.Error{Phase: "input", Err: err}
	}
	return core.TextWithTM(ctx, input, adapter, profile, store, command.scope, stdout)
}

func readTranslationInput(path string, limits config.Limits) (raw []byte, err error) {
	// O_NONBLOCK prevents a swapped FIFO from blocking between pathname lookup and fstat.
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errTranslationInput
	}
	raw, err = io.ReadAll(io.LimitReader(file, int64(limits.ArtifactBytes)+1))
	if err != nil {
		return nil, err
	}
	if err := limits.CheckArtifactBytes(len(raw)); err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) {
		return nil, errTranslationInput
	}
	return raw, nil
}
