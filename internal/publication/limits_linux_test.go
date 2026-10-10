//go:build linux

package publication

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
)

func TestSourceSnapshotBoundsAndExistingOutput(t *testing.T) {
	for _, n := range []int{3, 4, 5} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			dir := publicationFixture(t)
			source, output := filepath.Join(dir, "source"), filepath.Join(dir, "output")
			putFile(t, source, strings.Repeat("a", n))
			putFile(t, output, strings.Repeat("b", 20))
			limits := config.DefaultLimits()
			limits.ArtifactBytes = 4
			run, err := PrepareWithLimits(context.Background(), Options{source, output, Replace}, limits)
			if n <= 4 {
				if err != nil {
					t.Fatal(err)
				}
				defer closeRun(t, run)
				if len(run.SourceSnapshot()) != n {
					t.Fatal("snapshot differs")
				}
			} else if !errors.Is(err, config.ErrLimitExceeded) || run != nil {
				t.Fatalf("run=%v err=%v", run, err)
			}
			wantBytes(t, source, strings.Repeat("a", n))
			wantBytes(t, output, strings.Repeat("b", 20))
		})
	}
}

type unreadableSource struct{ t *testing.T }

func (r unreadableSource) Read([]byte) (int, error) {
	r.t.Fatal("read oversized stat")
	return 0, io.EOF
}
func TestSourceStatRejectsBeforeRead(t *testing.T) {
	if _, err := readSource(unreadableSource{t}, 5, 4); !errors.Is(err, config.ErrLimitExceeded) {
		t.Fatal(err)
	}
}

func TestSourceGrowthAfterStatIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	putFile(t, path, "abcd")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	appendFile, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = appendFile.WriteString(strings.Repeat("x", 100)); err != nil {
		t.Fatal(err)
	}
	if err = appendFile.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := readSource(f, stat.Size(), 4)
	offset, seekErr := f.Seek(0, io.SeekCurrent)
	if b != nil || !errors.Is(err, config.ErrLimitExceeded) || seekErr != nil || offset != 5 {
		t.Fatalf("bytes=%d err=%v read=%d seek=%v", len(b), err, offset, seekErr)
	}
}
