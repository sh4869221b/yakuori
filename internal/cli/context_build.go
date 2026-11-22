package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sh4869221b/yakuori/internal/adapter/genericjson"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/contextpack"
	"github.com/sh4869221b/yakuori/internal/validate"
	"golang.org/x/sys/unix"
)

func runContextBuild(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var scope contextpack.BuildScope
	var outputDir string
	var help strings.Builder
	flags := flag.NewFlagSet("context build", flag.ContinueOnError)
	flags.SetOutput(&help)
	flags.StringVar(&scope.SourceLanguage, "source-language", "", "required source language")
	flags.StringVar(&scope.TargetLanguage, "target-language", "", "required target language")
	flags.StringVar(&scope.GameNamespace, "game", "", "required game namespace")
	flags.StringVar(&scope.ArtifactNamespace, "artifact", "", "required artifact namespace")
	flags.StringVar(&outputDir, "output-dir", "", "existing directory for a new candidate file")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: yakuori context build [options] FILE")
		fmt.Fprintln(flags.Output(), "FILE must be a regular Generic JSON v1 file. Flags precede FILE.")
		fmt.Fprintln(flags.Output(), "\nOptions:")
		flags.PrintDefaults()
	}
	flagErr := flags.Parse(args)
	if flagErr == flag.ErrHelp {
		return writeOutput(stdout, stderr, help.String(), nil)
	}
	if flagErr != nil || flags.NArg() != 1 || flags.Arg(0) == "" || flags.Arg(0) == "-" ||
		scope.SourceLanguage == "" || scope.TargetLanguage == "" || scope.GameNamespace == "" ||
		scope.ArtifactNamespace == "" || outputDir == "" {
		return reportFailure(stderr, ExitUsage, "invalid context build arguments; use context build --help")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if ctx.Err() != nil {
		return reportFailure(stderr, ExitFailure, "context build: canceled")
	}

	limits := config.DefaultLimits()
	adapter, err := genericjson.New(genericjson.Metadata{
		SourceLanguage: scope.SourceLanguage, TargetLanguage: scope.TargetLanguage,
		GameNamespace: scope.GameNamespace, ArtifactNamespace: scope.ArtifactNamespace,
	}, limits)
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context build: "+err.Error())
	}
	raw, err := readTranslationInput(flags.Arg(0), limits)
	if err != nil {
		reason := "cannot read input file"
		var limitErr *config.LimitError
		if errors.As(err, &limitErr) {
			reason = limitErr.Error()
		} else if errors.Is(err, errTranslationInput) {
			reason = "input must be a regular UTF-8 file"
		}
		return reportFailure(stderr, ExitFailure, "context build: "+reason)
	}
	session, err := adapter.Import(raw, validate.Profile{})
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context build: "+err.Error())
	}
	doc, err := contextpack.Build(session, scope)
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context build: "+err.Error())
	}
	candidate, err := publishContextCandidate(ctx, outputDir, doc.Canonical)
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context build: "+err.Error())
	}
	result := fmt.Sprintf("candidate: %s\nfingerprint: %x\n", candidate, doc.Fingerprint)
	if n, err := io.WriteString(stdout, result); err != nil || n != len(result) {
		return reportFailure(stderr, ExitFailure, fmt.Sprintf("context build: candidate %q saved, but write stdout failed", candidate))
	}
	return ExitOK
}

func publishContextCandidate(ctx context.Context, dir string, canonical []byte) (candidate string, err error) {
	const stagePrefix = ".context-stage-"
	stage, err := os.CreateTemp(dir, stagePrefix+"*")
	if err != nil {
		return "", errors.New("cannot create candidate stage")
	}
	stagePath := stage.Name()
	defer func() {
		if candidate == "" {
			if removeErr := os.Remove(stagePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, errors.New("cannot remove candidate stage"))
			}
		}
	}()
	_, writeErr := stage.Write(canonical)
	closeErr := stage.Close()
	if writeErr != nil || closeErr != nil {
		return "", errors.New("cannot write candidate stage")
	}

	// Keep the stage directory path so the filesystem resolves symlink/.. consistently.
	directory, name := filepath.Split(stagePath)
	suffix := strings.TrimPrefix(name, stagePrefix)
	destination := directory + "context-candidate-" + suffix + ".json"
	// Rename is the commit point. A cancellation after it leaves the candidate visible.
	if ctx.Err() != nil {
		return "", errors.New("canceled")
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stagePath, unix.AT_FDCWD, destination, unix.RENAME_NOREPLACE); err != nil {
		return "", errors.New("cannot publish candidate")
	}
	return destination, nil
}
