// Package cli owns stdout/stderr and process exit status at the application edge.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/publication"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const usage = `Usage: yakuori <command>

Commands:
  help       Show this help
  doctor     Show resolved Yakuori storage directories (read-only)

Options:
  -h, --help Show this help

Foundation only: translation, models and storage are not implemented yet.
`

var errInvalidLocalizeArguments = errors.New("invalid localize arguments")
var errSameLocalizePath = errors.New("source and output are the same path")

// Run never exits the process. Only main calls os.Exit.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, func() (config.Paths, error) { return config.Resolve(os.Getenv) })
}

// resolve is the small fake-test seam; no engine or dependency framework yet.
func run(args []string, stdout, stderr io.Writer, resolve func() (config.Paths, error)) int {
	fail := func(code int, message string) int {
		// A failed diagnostic write must still return failure.
		_, _ = fmt.Fprintf(stderr, "yakuori: %s\n", message)
		return code
	}
	if len(args) == 0 {
		return fail(ExitUsage, "missing command; use --help")
	}
	if args[0] == "localize" {
		_, help, err := parseLocalize(args[1:])
		if err != nil {
			if errors.Is(err, errSameLocalizePath) {
				return fail(ExitUsage, "source and output are the same path; use --in-place")
			}
			return fail(ExitUsage, "invalid localize arguments; use --help")
		}
		if help == "" {
			return fail(ExitFailure, "localize pipeline is not implemented")
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
	case "doctor":
		paths, err := resolve()
		if err != nil {
			return fail(ExitFailure, "resolve storage paths: "+err.Error())
		}
		result = fmt.Sprintf("config: %s\ndata: %s\ncache: %s\nstate: %s\n", paths.Config, paths.Data, paths.Cache, paths.State)
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

func parseLocalize(args []string) (publication.Options, string, error) {
	var output string
	var replaceOutput, inPlace bool
	var outputSpecified, replaceSpecified, inPlaceSpecified bool
	var help strings.Builder
	flags := flag.NewFlagSet("localize", flag.ContinueOnError)
	flags.SetOutput(&help)
	flags.StringVar(&output, "output", "", "write the result to OUT")
	flags.BoolVar(&replaceOutput, "replace-output", false, "replace OUT atomically when it exists")
	flags.BoolVar(&inPlace, "in-place", false, "replace SOURCE while retaining an original backup")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: yakuori localize [options] SOURCE")
		fmt.Fprintln(flags.Output(), "\nOptions:")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return publication.Options{}, help.String(), nil
		}
		return publication.Options{}, "", errInvalidLocalizeArguments
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
		return publication.Options{}, "", errInvalidLocalizeArguments
	}
	if replaceSpecified && !outputSpecified {
		return publication.Options{}, "", errInvalidLocalizeArguments
	}
	if inPlace {
		if len(flags.Args()) != 1 || flags.Arg(0) == "" {
			return publication.Options{}, "", errInvalidLocalizeArguments
		}
		source := flags.Arg(0)
		return publication.Options{Source: source, Output: source, Mode: publication.InPlace}, "", nil
	}
	if !outputSpecified || output == "" || len(flags.Args()) != 1 || flags.Arg(0) == "" {
		return publication.Options{}, "", errInvalidLocalizeArguments
	}
	source := flags.Arg(0)
	sourceAbs, err := filepath.Abs(source)
	if err != nil {
		return publication.Options{}, "", errInvalidLocalizeArguments
	}
	outputAbs, err := filepath.Abs(output)
	if err != nil {
		return publication.Options{}, "", errInvalidLocalizeArguments
	}
	if sourceAbs == outputAbs {
		return publication.Options{}, "", errSameLocalizePath
	}
	mode := publication.Create
	if replaceOutput {
		mode = publication.Replace
	}
	return publication.Options{Source: source, Output: output, Mode: mode}, "", nil
}
