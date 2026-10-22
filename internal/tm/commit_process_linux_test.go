//go:build linux

package tm

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

type checkpointRow struct {
	Schema                int
	Source, Profile, Text string
}

func checkpointOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func checkpointCapture(t *testing.T, name string, data []byte) {
	t.Helper()
	if dir := os.Getenv("YAKUORI_PIPELINE_EVIDENCE"); dir != "" {
		dir = filepath.Join(dir, t.Name())
		checkpointOK(t, os.MkdirAll(dir, 0700))
		checkpointOK(t, os.WriteFile(filepath.Join(dir, name), data, 0600))
	}
}

func checkpointBatch(t *testing.T, s *Store) (*Run, []checkpointRow) {
	t.Helper()
	session, profile := runSession(t, strings.Fields("zero one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty twenty-one twenty-two twenty-three")...), runProfile(t, "checkpoint-model")
	r := beginFixture(t, s, session, profile, testScope())
	want := []checkpointRow{{1, "unrelated\x00source", "unrelated\x00profile", "retained\nexact"}}
	for i, u := range session.Units() {
		// Derive complete expected keys from input, not Run.keys or persisted rows.
		source, err := unit.NewSourceIdentity(session, u.ID(), "game", "artifact", nil)
		checkpointOK(t, err)
		key, err := profile.ExactKey(source)
		checkpointOK(t, err)
		want = append(want, checkpointRow{key.Schema(), string(key.SourceBytes()), string(key.ProfileBytes()), fmt.Sprintf("translation %02d ", i) + strings.Repeat("accepted ", 1024)})
	}
	return r, want
}

func checkpointAccepted(t *testing.T, r *Run) []validate.AcceptedTranslation {
	t.Helper()
	texts := make([]string, len(r.session.Units()))
	for i := range texts {
		texts[i] = fmt.Sprintf("translation %02d ", i) + strings.Repeat("accepted ", 1024)
	}
	return commitFixture(t, r, texts...)
}

func checkpointKill(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	checkpointOK(t, cmd.Process.Kill())
	var exit *exec.ExitError
	if err := cmd.Wait(); !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("expected SIGKILL: %v", err)
	}
}

func checkpointState(t *testing.T, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, want []checkpointRow) {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT key_schema,source_identity,translation_profile,translated_text FROM tm ORDER BY source_identity,translation_profile")
	checkpointOK(t, err)
	defer func() { checkpointOK(t, rows.Close()) }()
	got := []checkpointRow{}
	for rows.Next() {
		var row checkpointRow
		checkpointOK(t, rows.Scan(&row.Schema, &row.Source, &row.Profile, &row.Text))
		got = append(got, row)
	}
	checkpointOK(t, rows.Err())
	want = slices.Clone(want)
	slices.SortFunc(want, func(a, b checkpointRow) int { return strings.Compare(a.Source, b.Source) })
	data, err := json.MarshalIndent(got, "", "  ")
	checkpointOK(t, err)
	checkpointCapture(t, fmt.Sprintf("rows-%d.json", len(got)), data)
	data, err = json.MarshalIndent(want, "", "  ")
	checkpointOK(t, err)
	checkpointCapture(t, fmt.Sprintf("expected-%d.json", len(want)), data)
	if !slices.Equal(got, want) {
		t.Fatalf("complete TM mismatch: got %d rows, want %d; full readback captured", len(got), len(want))
	}
}

func TestPipelineTMCommitCheckpoint(t *testing.T) {
	for _, mode := range []string{"error", "cancel", "release"} {
		t.Run(mode, func(t *testing.T) { checkpointProcess(t, mode) })
	}
}

func TestPipelineTMCommitStoppedProcess(t *testing.T) {
	for _, mode := range []string{"before-commit", "after-commit"} {
		t.Run(mode, func(t *testing.T) { checkpointProcess(t, mode) })
	}
}

func checkpointProcess(t *testing.T, mode string) {
	// Given: a persisted D0, plus notice/release pipes before child launch.
	path, s := storeFixture(t)
	_, d1 := checkpointBatch(t, s)
	d0 := []checkpointRow{d1[0], d1[1]}
	d0[1].Text = ""
	for _, row := range d0 {
		storeExec(t, s.db, "INSERT INTO tm VALUES(?,?,?,?)", row.Schema, []byte(row.Source), []byte(row.Profile), row.Text)
	}
	checkpointState(t, s.db, d0)
	checkpointOK(t, s.Close())
	checkpointCapture(t, "original.sqlite", readBytes(t, path))
	notice, ready, err := os.Pipe()
	checkpointOK(t, err)
	defer func() { checkpointOK(t, notice.Close()); checkpointOK(t, ready.Close()) }()
	release, resume, err := os.Pipe()
	checkpointOK(t, err)
	defer func() { checkpointOK(t, release.Close()); checkpointOK(t, resume.Close()) }()
	checkpointOK(t, notice.SetReadDeadline(time.Now().Add(15*time.Second)))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.v", "-test.run=^TestPipelineTMCommitProcessHelper$")
	cmd.Env = append(os.Environ(), "YAKUORI_TM_CHECKPOINT_PATH="+path, "YAKUORI_TM_CHECKPOINT_MODE="+mode)
	cmd.ExtraFiles = []*os.File{ready, release}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	checkpointOK(t, cmd.Start())
	defer func() {
		if cmd.ProcessState == nil {
			checkpointKill(t, cmd)
		}
	}()
	// When: kill only after the dedicated notice identifies the boundary.
	var signal [1]byte
	_, err = io.ReadFull(notice, signal[:])
	checkpointOK(t, err)
	wantSignal := byte('R')
	if mode == "after-commit" {
		wantSignal = 'C'
	}
	if signal[0] != wantSignal {
		t.Fatalf("unexpected ready notice: %q", signal)
	}
	switch mode {
	case "before-commit", "after-commit":
		checkpointKill(t, cmd)
	default:
		_, err = resume.Write([]byte{'G'})
		checkpointOK(t, err)
		if err := cmd.Wait(); err != nil {
			t.Fatalf("checkpoint child: %v; %s", err, output.String())
		}
	}
	t.Logf("ready=%s PID=%d wait=%s reaped=true child=%s", mode, cmd.Process.Pid, cmd.ProcessState, output.String())
	checkpointCapture(t, "process.txt", fmt.Appendf(nil, "ready=%s token=%q PID=%d wait=%s reaped=true\n%s", mode, signal, cmd.Process.Pid, cmd.ProcessState, output.String()))
	// Then: snapshot raw assets before Open/Recover can touch SQLite.
	before := readBytes(t, path)
	checkpointCapture(t, "stopped.sqlite", before)
	if mode == "before-commit" {
		journal := readBytes(t, path+"-journal")
		checkpointCapture(t, "journal-before", journal)
		if len(journal) <= 512 || !bytes.Equal(journal[:8], []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}) || binary.BigEndian.Uint32(journal[8:12]) == 0 {
			t.Fatal("no real spilled hot-journal header/records")
		}
		if _, err := Open(t.Context(), path, config.DefaultLimits()); !errors.Is(err, ErrRecoveryRequired) {
			t.Fatalf("implicit recovery: %v", err)
		}
		assertPreserved(t, path, before)
		assertPreserved(t, path+"-journal", journal)
		t.Logf("hot journal=%d bytes header=%x; Open refused without mutation", len(journal), journal[:28])
		s, err = Recover(t.Context(), path, config.DefaultLimits())
	} else {
		s, err = Open(t.Context(), path, config.DefaultLimits())
	}
	checkpointOK(t, err)
	defer func() { checkpointOK(t, s.Close()) }()
	want := d1
	if mode == "before-commit" || mode == "error" || mode == "cancel" {
		want = d0
	}
	checkpointState(t, s.db, want)
	checkpointCapture(t, "reconciled.sqlite", readBytes(t, path))
	if _, err := os.Stat(path + "-journal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("journal unresolved: %v", err)
	}
	fresh, _ := checkpointBatch(t, s)
	commitRun(t, fresh, checkpointAccepted(t, fresh))
	checkpointState(t, s.db, d1)
	checkpointCapture(t, "final.sqlite", readBytes(t, path))
	t.Log("explicit fresh commit D1; store lock reacquired; no stage/output/backup/record (TM component)")
}

func TestPipelineTMCommitProcessHelper(t *testing.T) {
	path := os.Getenv("YAKUORI_TM_CHECKPOINT_PATH")
	if path == "" {
		return
	}
	s, err := Open(t.Context(), path, config.DefaultLimits())
	checkpointOK(t, err)
	defer func() { checkpointOK(t, s.Close()) }()
	storeExec(t, s.db, "PRAGMA cache_size=5")
	r, d1 := checkpointBatch(t, s)
	values := checkpointAccepted(t, r)
	notice, release := os.NewFile(3, "notice"), os.NewFile(4, "release")
	defer func() { checkpointOK(t, notice.Close()); checkpointOK(t, release.Close()) }()
	pause := func(token byte) error {
		if _, err := notice.Write([]byte{token}); err != nil {
			return err
		}
		var b [1]byte
		_, err := io.ReadFull(release, b[:])
		return err
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mode, injected, calls := os.Getenv("YAKUORI_TM_CHECKPOINT_MODE"), errors.New("checkpoint error"), 0
	err = r.commit(ctx, r.session, r.profile, values, func(ctx context.Context, c *sql.Conn) error {
		calls++
		checkpointState(t, c, d1)
		if mode != "after-commit" {
			if err := pause('R'); err != nil {
				return err
			}
		}
		switch mode {
		case "error":
			return injected
		case "cancel":
			cancel()
			<-ctx.Done()
		}
		return nil
	})
	var wantErr error
	switch mode {
	case "error":
		wantErr = injected
	case "cancel":
		wantErr = context.Canceled
	}
	if calls != 1 || !errors.Is(err, wantErr) {
		t.Fatalf("callback calls=%d commit error=%v want=%v", calls, err, wantErr)
	}
	t.Logf("commit returned=%v callback calls=%d", err, calls)
	if mode == "after-commit" {
		checkpointOK(t, pause('C')) // Only reached after actual COMMIT returned nil.
	}
}
