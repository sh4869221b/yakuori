// Command w3adapterqa checks production exports without a model or database.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/sh4869221b/yakuori/internal/adapter/w3strings"
	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/validate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Production export closed, reopened and compared successfully.")
}

func run(args []string) (err error) {
	flags := flag.NewFlagSet("w3adapterqa", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "source w3strings file")
	translations := flags.String("translations", "", "independent four-column CSV")
	output := flags.String("output", "", "new output file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || *translations == "" || *output == "" || flags.NArg() != 0 {
		return errors.New("require --input PATH --translations CSV --output NEWPATH")
	}
	// These are research-fixture input budgets, not product defaults or
	// translation-output limits.
	limits := config.DefaultLimits()
	limits.ArtifactBytes, limits.Units = 65536, 4095
	limits.UnitTextBytes, limits.TotalTextBytes, limits.Segments = 16384, 32768, 4095
	a, err := w3strings.NewWithLimits(limits)
	if err != nil {
		return err
	}
	source, err := os.Open(*input)
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(source, int64(limits.ArtifactBytes)+1))
	if err := errors.Join(readErr, source.Close()); err != nil {
		return err
	}
	profile, err := validate.NewProfile([32]byte{1})
	if err != nil {
		return err
	}
	session, err := a.Import(raw, profile)
	if err != nil {
		return err
	}
	csv, err := os.Open(*translations)
	if err != nil {
		return err
	}
	texts, readErr := readTranslations(csv)
	if err := errors.Join(readErr, csv.Close()); err != nil {
		return err
	}
	accepted := make([]validate.AcceptedTranslation, 0, session.UnitCount())
	for _, u := range session.Units() {
		text, exists := texts[u.ID().StableID()]
		if !exists {
			return errors.New("missing CSV ID")
		}
		translation, err := validate.Validate(session, u.ID(), profile, text)
		if err != nil {
			return err
		}
		accepted = append(accepted, translation)
		delete(texts, u.ID().StableID())
	}
	if len(texts) != 0 {
		return errors.New("extra CSV ID")
	}
	expected, err := artifact.PrepareExport(session, profile, accepted)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.Remove(*output))
		}
	}()
	buffer := bufio.NewWriter(file)
	manifest, exportErr := a.Export(buffer, session, profile, accepted)
	if exportErr == nil {
		exportErr = buffer.Flush()
	}
	if exportErr == nil {
		exportErr = file.Sync()
	}
	if err := errors.Join(exportErr, file.Close()); err != nil {
		return err
	}
	reopened, err := os.Open(*output)
	if err != nil {
		return err
	}
	observed, observeErr := a.Observe(reopened)
	if err := errors.Join(observeErr, reopened.Close()); err != nil {
		return err
	}
	return artifact.CompareFinal(manifest, expected, observed)
}

func readTranslations(reader io.Reader) (map[string]string, error) {
	texts := make(map[string]string)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.SplitN(line, "|", 4)
		if len(fields) != 4 {
			return nil, errors.New("CSV requires four columns")
		}
		id := strings.TrimSpace(fields[0])
		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return nil, errors.New("invalid CSV ID")
		}
		if _, exists := texts[id]; exists {
			return nil, errors.New("duplicate CSV ID")
		}
		texts[id] = fields[3]
	}
	return texts, scanner.Err()
}
