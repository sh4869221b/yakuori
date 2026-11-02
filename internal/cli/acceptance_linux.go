//go:build linux

package cli

import (
	"context"
	"io"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
)

// AcceptanceDetails binds evidence to the Engine used for the translation.
type AcceptanceDetails struct {
	ExpectedModelID     string                    `json:"-"`
	ExpectedModelSHA256 [32]byte                  `json:"-"`
	ModelID             string                    `json:"model_id"`
	Info                inference.ModelInfo       `json:"info"`
	Profile             [32]byte                  `json:"profile"`
	Policy              inference.EffectivePolicy `json:"policy"`
}

// RunW3Acceptance invokes the same parsed command and pipeline as the CLI.
// The caller supplies the registered model opener and isolated TM paths.
func RunW3Acceptance(ctx context.Context, args []string, paths config.Paths, open func(context.Context, string) (string, inference.Engine, error), observation *localize.AcceptanceObservation, details *AcceptanceDetails) (localize.Result, error) {
	command, _, err := parseTranslation("localize", args)
	if err != nil {
		return localize.Result{}, err
	}
	if command.format != "w3strings" || command.options.Mode != "create" {
		return localize.Result{}, errInvalidLocalizeArguments
	}
	return command.executeObserved(ctx, io.Discard, paths, open, observation, details)
}
