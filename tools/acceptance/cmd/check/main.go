// Command check validates the fixture set and candidate acceptance criteria.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/sh4869221b/yakuori/tools/acceptance"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	manifest := flag.String("manifest", "", "fixture manifest JSON")
	criteria := flag.String("criteria", "", "candidate criteria JSON")
	root := flag.String("root", "", "root for relative fixture paths")
	requireReady := flag.Bool("require-ready", false, "fail unless formal acceptance can start")
	flag.Parse()
	if *manifest == "" || *criteria == "" || *root == "" || flag.NArg() != 0 {
		return errors.New("require --manifest FILE --criteria FILE --root DIR")
	}
	var m acceptance.Manifest
	if err := acceptance.Load(*manifest, &m); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	var c acceptance.Criteria
	if err := acceptance.Load(*criteria, &c); err != nil {
		return fmt.Errorf("criteria: %w", err)
	}
	result, err := acceptance.Check(*root, m, c)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	if *requireReady && !result.AcceptanceReady {
		return errors.New("fixture set and criteria are not ready for formal acceptance")
	}
	return nil
}
