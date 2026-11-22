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

type contextBuildCommand struct {
	scope                              contextpack.BuildScope
	input, outputDir, backend, against string
	infer                              bool
}

func parseContextBuild(args []string) (contextBuildCommand, string, error) {
	var command contextBuildCommand
	scope := &command.scope
	var help strings.Builder
	flags := flag.NewFlagSet("context build", flag.ContinueOnError)
	flags.SetOutput(&help)
	flags.StringVar(&scope.SourceLanguage, "source-language", "", "required source language")
	flags.StringVar(&scope.TargetLanguage, "target-language", "", "required target language")
	flags.StringVar(&scope.GameNamespace, "game", "", "required game namespace")
	flags.StringVar(&scope.ArtifactNamespace, "artifact", "", "required artifact namespace")
	flags.StringVar(&command.outputDir, "output-dir", "", "existing directory for a new candidate file")
	flags.BoolVar(&command.infer, "infer", false, "generate additional candidates using the selected local model")
	flags.StringVar(&command.backend, "backend", "cpu", "inference backend: cpu, cuda or webgpu (requires --infer)")
	flags.StringVar(&command.against, "against", "", "compare with a validated pack and report collisions (requires --infer)")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: yakuori context build [options] FILE")
		fmt.Fprintln(flags.Output(), "FILE must be a regular Generic JSON v1 file. Flags precede FILE.")
		fmt.Fprintln(flags.Output(), "\nOptions:")
		flags.PrintDefaults()
	}
	flagErr := flags.Parse(args)
	if flagErr == flag.ErrHelp {
		return command, help.String(), nil
	}
	if flagErr != nil || flags.NArg() != 1 || flags.Arg(0) == "" || flags.Arg(0) == "-" ||
		scope.SourceLanguage == "" || scope.TargetLanguage == "" || scope.GameNamespace == "" ||
		scope.ArtifactNamespace == "" || command.outputDir == "" ||
		command.backend != "cpu" && command.backend != "cuda" && command.backend != "webgpu" {
		return command, "", errInvalidContextBuildArguments
	}
	var invalid bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "backend":
			invalid = invalid || !command.infer
		case "against":
			invalid = invalid || !command.infer || command.against == ""
		}
	})
	if invalid {
		return command, "", errInvalidContextBuildArguments
	}
	command.input = flags.Arg(0)
	return command, "", nil
}

var errInvalidContextBuildArguments = errors.New("invalid context build arguments; use context build --help")

func runContextBuild(ctx context.Context, args []string, stdout, stderr io.Writer, resolve func() (config.Paths, error), open selectedEngineOpener) int {
	command, help, err := parseContextBuild(args)
	if err != nil {
		return reportFailure(stderr, ExitUsage, err.Error())
	}
	if help != "" {
		return writeOutput(stdout, stderr, help, nil)
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if ctx.Err() != nil {
		return reportFailure(stderr, ExitFailure, "context build: canceled")
	}

	scope := command.scope
	limits := config.DefaultLimits()
	adapter, err := genericjson.New(genericjson.Metadata{
		SourceLanguage: scope.SourceLanguage, TargetLanguage: scope.TargetLanguage,
		GameNamespace: scope.GameNamespace, ArtifactNamespace: scope.ArtifactNamespace,
	}, limits)
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context build: "+err.Error())
	}
	raw, err := readTranslationInput(command.input, limits)
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
	var collisions string
	if command.infer {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, contextpack.InferTimeout)
		defer cancel()
		doc, collisions, err = inferContextPack(ctx, command, doc, session, resolve, open)
		if err != nil {
			return reportFailure(stderr, ExitFailure, "context build: "+err.Error())
		}
	}
	candidate, err := publishContextCandidate(ctx, command.outputDir, doc.Canonical)
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context build: "+err.Error())
	}
	result := fmt.Sprintf("candidate: %s\nfingerprint: %x\n%s", candidate, doc.Fingerprint, collisions)
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
