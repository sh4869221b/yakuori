// Package cli owns stdout/stderr and process exit status at the application edge.
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

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/models"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const usage = `Usage: yakuori <command>

Commands:
  help       Show this help
  doctor     Diagnose storage; --check-model loads the selected model
  models     Import, pull, select, or verify a local GGUF model
  translate  Translate a UTF-8 text file to stdout
  localize   Translate a UTF-8 text file to a published file

Options:
  -h, --help Show this help

Use <command> --help for options.
`

var errInvalidLocalizeArguments = errors.New("invalid localize arguments")
var errSameLocalizePath = errors.New("source and output are the same path")

// Run never exits the process. Only main calls os.Exit.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, func() (config.Paths, error) { return config.Resolve(os.Getenv) })
}

func run(args []string, stdout, stderr io.Writer, resolve func() (config.Paths, error)) int {
	return runWithPipeline(context.Background(), args, stdout, stderr, resolve, nil)
}

type fileRunner func(context.Context, publication.Options) (localize.Result, error)

func runWithPipeline(ctx context.Context, args []string, stdout, stderr io.Writer, resolve func() (config.Paths, error), runner fileRunner) int {
	fail := func(code int, message string) int {
		// A failed diagnostic write must still return failure.
		_, _ = fmt.Fprintf(stderr, "yakuori: %s\n", message)
		return code
	}
	if len(args) == 0 {
		return fail(ExitUsage, "missing command; use --help")
	}
	if args[0] == "models" || args[0] == "doctor" {
		return runStorage(ctx, args, stdout, stderr, resolve)
	}
	if args[0] == "localize" || args[0] == "translate" {
		command, help, err := parseTranslation(args[0], args[1:])
		if err != nil {
			if errors.Is(err, errSameLocalizePath) {
				return fail(ExitUsage, "source and output are the same path; use --in-place")
			}
			return fail(ExitUsage, "invalid "+args[0]+" arguments; use --help")
		}
		if help == "" {
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			if runner != nil && command.action == "localize" {
				result, err := runner(ctx, command.options)
				return reportPipeline(stderr, result, err)
			}
			paths, err := resolve()
			if err != nil {
				return reportPipeline(stderr, localize.Result{}, &localize.Error{Phase: "config", Err: err})
			}
			result, err := command.execute(ctx, stdout, paths, models.New(paths).OpenSelected)
			return reportPipeline(stderr, result, err)
		}
		n, err := io.WriteString(stdout, help)
		if err != nil || n != len(help) {
			return fail(ExitFailure, "write stdout failed")
		}
		return ExitOK
	}
	if len(args) != 1 {
		return fail(ExitUsage, "unexpected arguments; use --help")
	}
	var result string
	switch args[0] {
	case "help", "-h", "--help":
		result = usage
	default:
		// Do not echo user-supplied text into ordinary diagnostics.
		if strings.HasPrefix(args[0], "-") {
			return fail(ExitUsage, "unknown option; use --help")
		}
		return fail(ExitUsage, "unknown command; use --help")
	}
	n, err := io.WriteString(stdout, result)
	if err != nil || n != len(result) {
		return fail(ExitFailure, "write stdout failed")
	}
	return ExitOK
}

func reportPipeline(stderr io.Writer, result localize.Result, err error) int {
	if err == nil {
		return ExitOK
	}
	message := "pipeline failed"
	var failure *localize.Error
	if errors.As(err, &failure) {
		message = failure.Error()
	}
	message += fmt.Sprintf("; TMCommitted=%t", result.TMCommitted)
	if result.Publication.State != "" {
		message += fmt.Sprintf("; publication=%s", result.Publication.State)
	}
	var recovery *publication.RecoveryError
	if errors.As(err, &recovery) {
		message += fmt.Sprintf("; recovery reason=%s state=%s record=%s output=%s backup=%s", recovery.Reason,
			recovery.Result.State, recovery.Result.RecordPath, recovery.Result.OutputPath, recovery.Result.BackupPath)
	}
	if result.BytesWritten > 0 {
		message += fmt.Sprintf("; stdout bytes=%d cannot be retracted", result.BytesWritten)
	}
	_, _ = fmt.Fprintf(stderr, "yakuori: %s\n", message)
	return ExitFailure
}

type translationCommand struct {
	action, sourceLanguage, targetLanguage, backend string
	scope                                           tm.Scope
	options                                         publication.Options
}

func parseTranslation(action string, args []string) (translationCommand, string, error) {
	command := translationCommand{action: action}
	var output string
	var format string
	var replaceOutput, inPlace bool
	var outputSpecified, replaceSpecified, inPlaceSpecified bool
	var help strings.Builder
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(&help)
	flags.StringVar(&command.sourceLanguage, "source-language", "", "required source language")
	flags.StringVar(&command.targetLanguage, "target-language", "", "required target language")
	flags.StringVar(&command.scope.GameNamespace, "game", "", "required game namespace")
	flags.StringVar(&command.scope.ArtifactNamespace, "artifact", "", "required artifact namespace")
	flags.StringVar(&command.backend, "backend", "cpu", "cpu or cuda")
	if action == "localize" {
		flags.StringVar(&format, "format", "", "required format: text")
		flags.StringVar(&output, "output", "", "write the result to OUT")
		flags.BoolVar(&replaceOutput, "replace-output", false, "replace OUT atomically when it exists")
		flags.BoolVar(&inPlace, "in-place", false, "replace SOURCE while retaining an original backup")
	}
	flags.Usage = func() {
		fmt.Fprintf(flags.Output(), "Usage: yakuori %s [options] SOURCE\n", action)
		fmt.Fprintln(flags.Output(), "Flags precede SOURCE. SOURCE must be a regular UTF-8 text file; stdin is unsupported.")
		fmt.Fprintln(flags.Output(), "\nOptions:")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return command, help.String(), nil
		}
		return command, "", errInvalidLocalizeArguments
	}
	if flags.NArg() != 1 || flags.Arg(0) == "" || flags.Arg(0) == "-" ||
		command.sourceLanguage == "" || command.targetLanguage == "" ||
		command.scope.GameNamespace == "" || command.scope.ArtifactNamespace == "" ||
		command.backend != "cpu" && command.backend != "cuda" || action == "localize" && format != "text" {
		return command, "", errInvalidLocalizeArguments
	}
	command.options.Source = flags.Arg(0)
	if action == "translate" {
		return command, "", nil
	}
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "output":
			outputSpecified = true
		case "replace-output":
			replaceSpecified = true
		case "in-place":
			inPlaceSpecified = true
		}
	})
	if inPlaceSpecified && (outputSpecified || replaceSpecified) {
		return command, "", errInvalidLocalizeArguments
	}
	if replaceSpecified && !outputSpecified {
		return command, "", errInvalidLocalizeArguments
	}
	if inPlace {
		source := flags.Arg(0)
		command.options = publication.Options{Source: source, Output: source, Mode: publication.InPlace}
		return command, "", nil
	}
	if !outputSpecified || output == "" || len(flags.Args()) != 1 || flags.Arg(0) == "" {
		return command, "", errInvalidLocalizeArguments
	}
	source := flags.Arg(0)
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return command, "", errInvalidLocalizeArguments
	}
	outputAbs, err := filepath.Abs(output)
	if err != nil {
		return command, "", errInvalidLocalizeArguments
	}
	if sourceAbs == outputAbs {
		return command, "", errSameLocalizePath
	}
	mode := publication.Create
	if replaceOutput {
		mode = publication.Replace
	}
	command.options = publication.Options{Source: source, Output: output, Mode: mode}
	return command, "", nil
}
