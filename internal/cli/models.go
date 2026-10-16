package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
	"github.com/sh4869221b/yakuori/internal/models"
)

const modelsUsage = `Usage: yakuori models import --id ID FILE
       yakuori models pull --id ID --sha256 HEX --size BYTES --digest-source TEXT URL
       yakuori models use ID
       yakuori models verify ID

Import keeps a private copy and does not select it. Flags precede FILE.
Pull fetches URL once over HTTPS and registers the received bytes only when
they match the expected size and SHA-256. It selects nothing, never retries
and leaves leftovers of an interrupted run alone. Flags precede URL.
`

const doctorUsage = `Usage: yakuori doctor [--check-model [--backend cpu|cuda]]

Without --check-model, inspect storage without creating files or loading models.
--check-model loads the selected model, reports Engine.Info, and closes it without generation.
The private cache/model-snapshots directory must already exist.
CPU is the default. CUDA requires -tags cuda on linux/amd64 and an NVIDIA driver (libcuda.so.1).
`

type storageCommand struct {
	action, id, source, backend string
	url, sha256, digestSource   string
	size                        int64
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
		if command.action != "import" && command.action != "pull" && command.action != "use" && command.action != "verify" {
			return command, flag.ErrHelp
		}
	}
	flags := flag.NewFlagSet(command.action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	if command.action == "import" {
		flags.StringVar(&command.id, "id", "", "model ID")
	}
	if command.action == "pull" {
		flags.StringVar(&command.id, "id", "", "model ID to register")
		flags.StringVar(&command.sha256, "sha256", "", "expected SHA-256 of the received bytes")
		flags.Int64Var(&command.size, "size", 0, "expected size of the received bytes")
		flags.StringVar(&command.digestSource, "digest-source", "", "where the expected digest came from")
	}
	if command.action == "doctor" {
		flags.BoolVar(&command.checkModel, "check-model", false, "load selected model")
		flags.StringVar(&command.backend, "backend", "cpu", "cpu or cuda")
	}
	if err := flags.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if flags.NArg() != 0 {
				return command, err
			}
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
	if command.action == "pull" {
		if flags.NArg() != 1 || flags.Arg(0) == "" {
			return command, flag.ErrHelp
		}
		command.url = flags.Arg(0)
		return command, validatePullArguments(command)
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

// validatePullArguments rejects an unusable pull before the storage paths are
// resolved, so an invalid request never reads storage, writes a file or opens
// a connection. internal/models validates the same contract again at the
// acquisition boundary.
func validatePullArguments(command storageCommand) error {
	if err := config.ValidateModelID(command.id); err != nil {
		return flag.ErrHelp
	}
	if len(command.sha256) != 64 || strings.ToLower(command.sha256) != command.sha256 {
		return flag.ErrHelp
	}
	if _, err := hex.DecodeString(command.sha256); err != nil {
		return flag.ErrHelp
	}
	if command.size <= 0 || command.size == math.MaxInt64 {
		return flag.ErrHelp
	}
	if strings.TrimSpace(command.digestSource) == "" {
		return flag.ErrHelp
	}
	parsed, err := url.Parse(command.url)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return flag.ErrHelp
	}
	return nil
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
	if command.action == "pull" {
		// The acquisition notice reaches stderr before the connection is
		// opened, so an unusable diagnostic stream stops the pull before any
		// network access.
		notice := fmt.Sprintf("pull id=%q source_url=%q expected_sha256=%s expected_size_bytes=%d digest_source=%q\n", command.id, command.url, command.sha256, command.size, command.digestSource)
		if n, writeErr := io.WriteString(stderr, notice); writeErr != nil || n != len(notice) {
			return storageFailure(stderr, ExitFailure, "write pull metadata failed")
		}
		// Only this dispatch maps SIGINT/SIGTERM onto the caller's context, and
		// a cancelled pull keeps the existing operational exit status.
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		entry, err := registry.Pull(ctx, models.PullRequest{URL: command.url, ID: command.id, SHA256: command.sha256, SizeBytes: command.size, DigestSource: command.digestSource})
		return reportPull(stdout, stderr, command.id, entry, err)
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

// reportPull writes the result of one pull to stdout. A non-zero entry means the
// registry commit already happened, so a failure that follows it is reported as
// registered: a stdout or acquisition failure must never make the caller believe
// that nothing was registered.
func reportPull(stdout, stderr io.Writer, id string, entry models.Entry, err error) int {
	if entry == (models.Entry{}) {
		return storageFailure(stderr, ExitFailure, "pull model: "+pullDiagnostic(err))
	}
	result := fmt.Sprintf("pull: %s\nmodel_sha256: %s\nsize_bytes: %d\n", id, entry.SHA256, entry.SizeBytes)
	if n, writeErr := io.WriteString(stdout, result); writeErr != nil || n != len(result) {
		return storageFailure(stderr, ExitFailure, fmt.Sprintf("pull model %q: registered, but write stdout failed", id))
	}
	if err != nil {
		return storageFailure(stderr, ExitFailure, fmt.Sprintf("pull model %q: registered, but %s", id, pullDiagnostic(err)))
	}
	return ExitOK
}

// pullDiagnostic maps an acquisition failure to a known reason. A raw HTTP/TLS
// error, a response body and model or registry text may carry private data, so
// only classified reasons reach ordinary diagnostics.
func pullDiagnostic(err error) string {
	if errors.Is(err, context.Canceled) {
		return "pull was cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "pull exceeded its deadline"
	}
	if errors.Is(err, models.ErrBusy) || errors.Is(err, models.ErrCollision) {
		return modelReason(err)
	}
	var transport *url.Error
	if errors.As(err, &transport) {
		return "source is unreachable, refused the connection, or failed TLS verification"
	}
	// The acquisition stage reports its own failures with these fixed phrases.
	text := err.Error()
	switch {
	case strings.Contains(text, "body exceeds the expected"):
		return "source sent more bytes than the expected size"
	case strings.Contains(text, "bytes, expected"):
		return "received size does not match the expected size"
	case strings.Contains(text, "expected SHA-256"):
		return "received bytes do not match the expected SHA-256"
	case strings.Contains(text, "unexpected status"):
		return "source did not answer with HTTP 200"
	}
	if reason := modelReason(err); reason != "" {
		return reason
	}
	return "pull failed for an unknown reason"
}

// Registry JSON and model metadata may contain user text; expose only known reasons.
func modelDiagnostic(err error) string {
	if reason := modelReason(err); reason != "" {
		return reason
	}
	return "invalid or unreadable model, configuration, or registry"
}

// modelReason names a known storage or loader failure, and returns "" when the
// reason is unknown so the caller can add its own fallback.
func modelReason(err error) string {
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
	return ""
}
