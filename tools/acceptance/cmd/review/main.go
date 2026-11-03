//go:build linux

// Command review generates private review sheets and public-safe summaries.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sh4869221b/yakuori/tools/acceptance"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func readJSON(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid JSON document")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON document")
	}
	return nil
}

type sheetPaths []string

func (s *sheetPaths) String() string        { return fmt.Sprint([]string(*s)) }
func (s *sheetPaths) Set(path string) error { *s = append(*s, path); return nil }

func run(args []string) error {
	if len(args) == 0 || (args[0] != "generate" && args[0] != "summarize") {
		return errors.New("use generate or summarize")
	}
	command := args[0]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	manifest := flags.String("manifest", "", "fixture manifest JSON")
	criteria := flags.String("criteria", "", "criteria JSON")
	runFile := flags.String("run", "", "producer run.json")
	root := flags.String("root", "", "fixture root")
	runID := flags.String("run-id", "", "one fixture run ID")
	var sheetFiles sheetPaths
	flags.Var(&sheetFiles, "sheet", "private review sheet JSON (repeat for summarize)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *manifest == "" || *criteria == "" || *runFile == "" || *root == "" || len(sheetFiles) == 0 || flags.NArg() != 0 || (command == "generate" && (*runID == "" || len(sheetFiles) != 1)) || (command == "summarize" && *runID != "") {
		return errors.New("require --manifest, --criteria, --run, --root and --sheet; generate also requires --run-id")
	}
	var m acceptance.Manifest
	var c acceptance.Criteria
	var r acceptance.RunRecord
	if err := acceptance.Load(*manifest, &m); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if err := acceptance.Load(*criteria, &c); err != nil {
		return fmt.Errorf("criteria: %w", err)
	}
	if err := readJSON(*runFile, &r); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	if command == "generate" {
		expected, _, err := acceptance.GenerateReview(m, c, r, *root, filepath.Dir(*runFile), *runID)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(sheetFiles[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return errors.Join(enc.Encode(expected), f.Close())
	}
	var summaries []acceptance.ReviewSummary
	var checked acceptance.Result
	for _, path := range sheetFiles {
		var actual acceptance.ReviewSheet
		if err := readJSON(path, &actual); err != nil {
			return fmt.Errorf("review sheet: %w", err)
		}
		expected, result, err := acceptance.GenerateReview(m, c, r, *root, filepath.Dir(*runFile), actual.RunID)
		if err != nil {
			return err
		}
		checked = result
		summary, err := acceptance.SummarizeReview(expected, checked, actual)
		if err != nil {
			return err
		}
		summaries = append(summaries, summary)
	}
	collection, err := acceptance.CollectReviews(m, c.FixtureIDs, checked, summaries)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(collection)
}
