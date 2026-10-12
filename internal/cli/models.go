package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
	"github.com/sh4869221b/yakuori/internal/models"
)

const modelsUsage = `Usage: yakuori models import --id ID FILE
       yakuori models use ID
       yakuori models verify ID

Import keeps a private copy and does not select it. Flags precede FILE.
`

const doctorUsage = `Usage: yakuori doctor [--check-model [--backend cpu|cuda]]

Without --check-model, inspect storage without creating files or loading models.
--check-model loads the selected model, reports Engine.Info, and closes it without generation.
The private cache/model-snapshots directory must already exist.
CPU is the default. CUDA requires -tags cuda on linux/amd64 and an NVIDIA driver (libcuda.so.1).
`

type storageCommand struct {
	action, id, source, backend string
	checkModel                  bool
	help                        string
}

func parseStorage(args []string) (storageCommand, error) {
	command := storageCommand{action: args[0], backend: "cpu"}
	rest := args[1:]
	if command.action == "models" {
		if len(rest) == 0 || rest[0] == "--help" || rest[0] == "-h" {
			if len(rest) > 1 {
				return command, flag.ErrHelp
			}
			command.help = modelsUsage
			return command, nil
		}
		command.action, rest = rest[0], rest[1:]
		if command.action != "import" && command.action != "use" && command.action != "verify" {
			return command, flag.ErrHelp
		}
	}
	flags := flag.NewFlagSet(command.action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if command.action == "import" {
		flags.StringVar(&command.id, "id", "", "model ID")
	}
	if command.action == "doctor" {
		flags.BoolVar(&command.checkModel, "check-model", false, "load selected model")
		flags.StringVar(&command.backend, "backend", "cpu", "cpu or cuda")
	}
	if err := flags.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			command.help = modelsUsage
			if command.action == "doctor" {
				command.help = doctorUsage
			}
			return command, nil
		}
		return command, err
	}
	if command.action == "doctor" {
		backendSpecified := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "backend" {
				backendSpecified = true
			}
		})
		if flags.NArg() != 0 || backendSpecified && !command.checkModel || command.backend != "cpu" && command.backend != "cuda" {
			return command, flag.ErrHelp
		}
		return command, nil
	}
	if flags.NArg() != 1 || flags.Arg(0) == "" {
		return command, flag.ErrHelp
	}
	if command.action == "import" {
		command.source = flags.Arg(0)
	} else {
		command.id = flags.Arg(0)
	}
	return command, config.ValidateModelID(command.id)
}

func runStorage(ctx context.Context, args []string, stdout, stderr io.Writer, resolve func() (config.Paths, error)) int {
	command, err := parseStorage(args)
	if err != nil {
		return storageFailure(stderr, ExitUsage, "invalid storage command arguments; use --help")
	}
	if command.help != "" {
		return storageOutput(stdout, stderr, command.help, nil)
	}
	paths, err := resolve()
	if err != nil {
		return storageFailure(stderr, ExitFailure, "resolve storage paths: "+err.Error())
	}
	registry := models.New(paths)
	if command.action == "doctor" {
		result, err := inspectStorage(paths, registry)
		if err == nil && command.checkModel {
			var metadata string
			metadata, err = checkModel(ctx, command.backend, registry.OpenSelected)
			result += metadata
		}
		return storageOutput(stdout, stderr, result, err)
	}
	var entry models.Entry
	switch command.action {
	case "import":
		entry, err = registry.Import(ctx, command.id, command.source)
	case "verify":
		entry, err = registry.Verify(ctx, command.id)
	case "use":
		err = registry.Use(ctx, command.id)
	}
	if err != nil {
		return storageFailure(stderr, ExitFailure, command.action+" model: "+modelDiagnostic(err))
	}
	result := fmt.Sprintf("%s: %s\n", command.action, command.id)
	if command.action != "use" {
		result += fmt.Sprintf("model_sha256: %s\nsize_bytes: %d\n", entry.SHA256, entry.SizeBytes)
	}
	return storageOutput(stdout, stderr, result, nil)
}

func storageFailure(stderr io.Writer, code int, message string) int {
	_, _ = fmt.Fprintln(stderr, "yakuori: "+message)
	return code
}

func storageOutput(stdout, stderr io.Writer, result string, diagnostic error) int {
	n, err := io.WriteString(stdout, result)
	if err != nil || n != len(result) {
		return storageFailure(stderr, ExitFailure, "write stdout failed")
	}
	if diagnostic != nil {
		return storageFailure(stderr, ExitFailure, diagnostic.Error())
	}
	return ExitOK
}

// Registry JSON and model metadata may contain user text; expose only known reasons.
func modelDiagnostic(err error) string {
	for _, reason := range []error{models.ErrBusy, models.ErrCollision, goinfer.ErrUnknownTemplate, goinfer.ErrDeclinedTokenizer, goinfer.ErrUnknownStop, goinfer.ErrUnknownContext, goinfer.ErrUnsupportedQuant} {
		if errors.Is(err, reason) {
			return reason.Error()
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return "required model or storage file is missing"
	}
	if errors.Is(err, os.ErrPermission) {
		return "storage access denied"
	}
	if errors.Is(err, goinfer.ErrUnsupportedBackend) || errors.Is(err, goinfer.ErrCUDAResidency) {
		return err.Error()
	}
	if strings.Contains(err.Error(), "identity mismatch") || strings.Contains(err.Error(), "model SHA-256 mismatch") {
		return "model identity mismatch"
	}
	if err.Error() == "no model selected" {
		return "no model selected; use models use ID"
	}
	return "invalid or unreadable model, configuration, or registry"
}
