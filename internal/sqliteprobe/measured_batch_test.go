package sqliteprobe

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in experiment stores measured translations in test-owned databases.
// It measures the existing SQLite transaction probe, not production TM behavior.
func TestMeasuredTranslationBatches(t *testing.T) {
	root := os.Getenv("YAKUORI_MEASURED_TRANSLATIONS")
	if root == "" {
		t.Skip("set YAKUORI_MEASURED_TRANSLATIONS to natural translation results")
	}
	paths, err := filepath.Glob(filepath.Join(root, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	batches := 0
	for _, path := range paths {
		if filepath.Base(path) == "supervisor.json" {
			continue
		}
		var report struct {
			Error string `json:"error"`
			Runs  []struct {
				Name     string   `json:"name"`
				Error    string   `json:"error"`
				Accepted int      `json:"accepted_units"`
				Output   []string `json:"output"`
			} `json:"runs"`
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		const reportLimit = 2 << 20
		data, readErr := io.ReadAll(io.LimitReader(file, reportLimit+1))
		if err := errors.Join(readErr, file.Close()); err != nil {
			t.Fatal(err)
		}
		if len(data) > reportLimit {
			t.Fatalf("research report exceeds 2 MiB: %s", path)
		}
		// A watchdog interruption can leave no JSON; it supplies no accepted batch.
		if len(data) == 0 {
			continue
		}
		if err := json.Unmarshal(data, &report); err != nil {
			t.Fatal(err)
		}
		if report.Error != "" {
			continue
		}
		for _, run := range report.Runs {
			if run.Name != "warm" || run.Error != "" {
				continue
			}
			if run.Accepted == 0 || run.Accepted != len(run.Output) {
				t.Fatalf("invalid accepted batch in %s", path)
			}
			batches++
			t.Run(filepath.Base(path), func(t *testing.T) {
				// Given one successful measured batch and a fresh probe database.
				_, db := fixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), operationLimit)
				defer cancel()
				start := time.Now()
				// When the batch is inserted and committed under the existing finite budget.
				err := transaction(ctx, db, func(c *sql.Conn) error {
					for i, text := range run.Output {
						if _, err := c.ExecContext(ctx, insertSQL, []byte(fmt.Sprintf("measured-unit-%04d", i)), []byte("measured-research-profile"), text); err != nil {
							return err
						}
					}
					return nil
				})
				elapsed := time.Since(start)
				// Then every measured translation is committed, with no partial batch.
				if err != nil {
					t.Fatal(err)
				}
				if got := count(t, db); got != len(run.Output) {
					t.Fatalf("rows=%d want=%d", got, len(run.Output))
				}
				bytes := 0
				for _, text := range run.Output {
					bytes += len(text)
				}
				t.Logf(`{"fixture":%q,"rows":%d,"translated_bytes":%d,"transaction_commit_ms":%.6f,"busy_ms":%d,"operation_ms":%d,"cleanup_ms":%d}`, filepath.Base(path), len(run.Output), bytes, float64(elapsed)/float64(time.Millisecond), busyMS, operationLimit.Milliseconds(), operationLimit.Milliseconds())
			})
		}
	}
	if batches == 0 {
		t.Fatal("no successful measured warm batches")
	}
}
