package cli

import (
	"context"
	"errors"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/models"
	"github.com/sh4869221b/yakuori/internal/unit"
)

func inferContextPack(ctx context.Context, command contextBuildCommand, doc *contextpack.Document, session unit.Session, resolve func() (config.Paths, error), open selectedEngineOpener) (*contextpack.Document, string, error) {
	var against *contextpack.Document
	if command.against != "" {
		var err error
		against, err = readContextPackFile(command.against)
		if err != nil {
			return nil, "", errors.New("against pack: " + err.Error())
		}
		if against.Pack.Scope.GameNamespace != doc.Pack.Scope.GameNamespace ||
			against.Pack.SourceLanguage != doc.Pack.SourceLanguage || against.Pack.TargetLanguage != doc.Pack.TargetLanguage {
			return nil, "", errors.New("against pack scope or languages do not match")
		}
	}
	paths, err := resolve()
	if err != nil {
		return nil, "", errors.New("cannot resolve model configuration")
	}
	if open == nil {
		open = models.New(paths).OpenSelected
	}
	if ctx.Err() != nil {
		return nil, "", errors.New("context inference canceled or timed out")
	}
	id, engine, err := open(ctx, command.backend)
	if err != nil {
		return nil, "", errors.New("cannot open the selected local inference model")
	}
	contextEngine, ok := engine.(contextpack.ContextEngine)
	if !ok {
		_ = engine.Close()
		return nil, "", errors.New("selected model cannot prepare context inference")
	}
	candidate, err := contextpack.Infer(ctx, doc, session, id, contextEngine)
	closeErr := engine.Close()
	if err != nil {
		return nil, "", err
	}
	if closeErr != nil {
		return nil, "", errors.New("cannot close the selected local inference model")
	}
	if ctx.Err() != nil {
		return nil, "", errors.New("context inference canceled or timed out")
	}
	if against == nil {
		return candidate, "", nil
	}
	return candidate, contextpack.Collisions(candidate.Pack, against.Pack), nil
}
