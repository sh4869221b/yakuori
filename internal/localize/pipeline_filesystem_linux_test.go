//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"golang.org/x/sys/unix"
)

func pipeline72NewFixture(t *testing.T, kind string) *pipelineFixture {
	t.Helper()
	input, ok := pipelineHarnessInput(t, kind)
	if !ok {
		t.Fatalf("unknown fixture kind %q", kind)
	}
	return newPipelineFixture(t, input)
}

func pipelineEqual(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values differ: got %#v, want %#v", got, want)
	}
}

func pipeline72SetPaths(f *pipelineFixture, paths publication.PipelineFaultPaths) {
	f.stage, f.record, f.backup = paths.Stage, paths.Record, paths.Backup
}

func pipeline72Paths(f *pipelineFixture) publication.PipelineFaultPaths {
	return publication.PipelineFaultPaths{Stage: f.stage, Record: f.record, Backup: f.backup}
}

func pipeline72Execute(f *pipelineFixture, ctx context.Context, options publication.Options, hooks publication.PipelineFaultHooks) (Result, error) {
	return f.core.fileWithTM(ctx, options, f.adapter, f.profile, nil, &tmExecution{store: f.store, scope: f.scope},
		func(ctx context.Context, options publication.Options) (publicationRun, error) {
			run, err := publication.PrepareWithLimits(ctx, options, f.limits)
			if err != nil {
				return nil, err
			}
			pipeline72SetPaths(f, run.SetPipelineFaultHooks(hooks))
			return run, nil
		})
}

var pipelineFileModes = []struct {
	name string
	mode publication.Mode
}{
	{"create", publication.Create},
	{"replace-absent", publication.Replace},
	{"replace-existing", publication.Replace},
	{"in-place", publication.InPlace},
}

type pipelineFileIdentity struct {
	Mount, Inode, Size uint64
	Major, Minor       uint32
	Mode               uint16
	Links              uint32
}

type pipelineFileImage struct {
	Path    string
	Present bool
	Bytes   []byte
	Link    string
	ID      pipelineFileIdentity
}

// Mirrors the existing record contract solely to build independent expectations.
type pipelineRecordID struct {
	Dev     uint64 `json:"dev"`
	Ino     uint64 `json:"ino"`
	MountID uint64 `json:"mount_id"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	Nlink   uint64 `json:"nlink"`
}

type pipelineRecordPath struct {
	Path     string `json:"path"`
	Basename string `json:"basename"`
}

type pipelineRecordFile struct {
	Path     string           `json:"path"`
	Basename string           `json:"basename"`
	Identity pipelineRecordID `json:"identity"`
	SHA256   string           `json:"sha256"`
	Size     int64            `json:"size"`
}

type pipelineOperationRecord struct {
	SchemaVersion  int                 `json:"schema_version"`
	RunID          string              `json:"run_id"`
	Mode           publication.Mode    `json:"mode"`
	Source         pipelineRecordFile  `json:"source"`
	Output         pipelineRecordPath  `json:"output"`
	Stage          pipelineRecordFile  `json:"stage"`
	Backup         *pipelineRecordPath `json:"backup"`
	SourceParent   pipelineRecordID    `json:"source_parent"`
	OutputParent   pipelineRecordID    `json:"output_parent"`
	ExistingOutput struct {
		Absent bool                `json:"absent"`
		File   *pipelineRecordFile `json:"file"`
	} `json:"existing_output"`
	Phase                  string `json:"phase"`
	LastCompletedOperation string `json:"last_completed_operation"`
}

func pipelineRecordedID(image pipelineFileImage) pipelineRecordID {
	id := image.ID
	return pipelineRecordID{unix.Mkdev(id.Major, id.Minor), id.Inode, id.Mount, int64(id.Size), uint32(id.Mode), uint64(id.Links)}
}

func pipelineRecordedFile(image pipelineFileImage) pipelineRecordFile {
	// SHA256 is the existing publication-record integrity contract, not a new receipt.
	return pipelineRecordFile{image.Path, filepath.Base(image.Path), pipelineRecordedID(image),
		fmt.Sprintf("%x", sha256.Sum256(image.Bytes)), int64(len(image.Bytes))}
}

func pipelineRunFD(t *testing.T, path string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		target, err := os.Readlink("/proc/self/fd/" + entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue // ReadDir's own descriptor has already closed.
		}
		if err != nil {
			t.Fatal(err)
		}
		if target != path {
			continue
		}
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			t.Fatal(err)
		}
		image := pipelineImage(t, path)
		pipelineEqual(t, uint64(st.Ino), image.ID.Inode)
		pipelineEqual(t, uint64(st.Dev), unix.Mkdev(image.ID.Major, image.ID.Minor))
		pipelineJSON(t, fmt.Sprintf("run-fd-%d-before.json", os.Getpid()), struct {
			PID, FD int
			File    pipelineFileImage
		}{os.Getpid(), fd, image})
		return fd
	}
	t.Fatalf("prepared Run directory descriptor not observed: %s", path)
	return -1
}

func pipelineRunClosed(t *testing.T, run *publication.Run, fd int) {
	t.Helper()
	_, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	runtime.KeepAlive(run) // An os.File finalizer must not conceal missing Close.
	target := ""
	if err == nil {
		var linkErr error
		target, linkErr = os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
		if linkErr != nil {
			t.Fatal(linkErr)
		}
	}
	pipelineJSON(t, fmt.Sprintf("run-fd-%d-after.json", os.Getpid()), struct {
		PID, FD int
		Error   string
		Target  string
	}{os.Getpid(), fd, fmt.Sprint(err), target})
	if !errors.Is(err, unix.EBADF) {
		t.Errorf("prepared Run directory FD remained open at Core return: fd=%d target=%s error=%v", fd, target, err)
	}
}

func pipelineImage(t *testing.T, path string) pipelineFileImage {
	t.Helper()
	image := pipelineFileImage{Path: path}
	if path == "" {
		return image
	}
	var st unix.Statx_t
	err := unix.Statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &st)
	if errors.Is(err, unix.ENOENT) {
		return image
	}
	if err != nil {
		t.Fatal(err)
	}
	if st.Mask&unix.STATX_MNT_ID == 0 {
		t.Fatal("mount identity unavailable")
	}
	image.Present = true
	image.ID = pipelineFileIdentity{st.Mnt_id, st.Ino, st.Size, st.Dev_major, st.Dev_minor, st.Mode, st.Nlink}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		image.Bytes = fileBytes(t, path)
	case unix.S_IFLNK:
		image.Link, err = os.Readlink(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	return image
}

func pipelineImages(t *testing.T, paths []string) []pipelineFileImage {
	t.Helper()
	var images []pipelineFileImage
	for _, path := range paths {
		images = append(images, pipelineImage(t, path))
	}
	return images
}

func pipelineJSON(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	pipelineFixtureSave(t, name, raw)
}

func pipelineBoundaryState(t *testing.T, f *pipelineFixture, options publication.Options, result Result, err error) {
	t.Helper()
	message := ""
	if err != nil {
		message = err.Error()
	}
	paths := []string{options.Source, options.Output, f.stage, f.record, f.backup}
	images := pipelineImages(t, paths)
	for i, image := range images {
		if image.Present && image.ID.Mode&unix.S_IFMT == unix.S_IFREG {
			pipelineFixtureSave(t, fmt.Sprintf("actual-%d.bin", i), image.Bytes)
		}
	}
	pipelineJSON(t, "state.json", struct {
		PID, UID int
		Layer    string
		Options  publication.Options
		Result   Result
		Error    string
		Files    []pipelineFileImage
		Rows     []pipelineFixtureRow
	}{os.Getpid(), os.Geteuid(), "whole Core; real Adapter/SQLite; controlled common Engine", options, result, message, images, pipelineFixtureReadRows(t, f.db)})
}

func pipelineOptions(t *testing.T, f *pipelineFixture, name string) publication.Options {
	t.Helper()
	for _, mode := range pipelineFileModes {
		if mode.name == name {
			pipeline72SetPaths(f, publication.PipelineFaultPaths{})
			parent := t.TempDir()
			options := publication.Options{Source: filepath.Join(parent, "source"), Output: filepath.Join(parent, "output"), Mode: mode.mode}
			if mode.mode == publication.InPlace {
				options.Output = options.Source
			}
			writeFile(t, options.Source, f.input.source)
			if name == "replace-existing" {
				writeFile(t, options.Output, []byte("Old output O.\n"))
			}
			return options
		}
	}
	t.Fatalf("unknown mode %q", name)
	return publication.Options{}
}

func pipelineWantTranslated(t *testing.T, f *pipelineFixture, path string) {
	t.Helper()
	raw := fileBytes(t, path)
	observed, err := f.adapter.Observe(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, u := range observed.Units {
		got[u.ID.StableID()] = u.Text
	}
	pipelineEqual(t, got, f.targets)
	// W3 retains the authored binary table order, while the independent CSV
	// expectations use a different order. Compare the complete multiset.
	gotKeys, wantKeys := make(map[artifact.KeyRelation]int), make(map[artifact.KeyRelation]int)
	for _, key := range observed.Keys {
		gotKeys[key]++
	}
	for _, key := range f.relations {
		wantKeys[key]++
	}
	pipelineEqual(t, gotKeys, wantKeys)
	pipelineFixtureSave(t, "validated-artifact.bin", raw)
}

func pipelineLocksFree(t *testing.T, parents ...string) {
	t.Helper()
	for _, parent := range parents {
		file, err := os.Open(parent)
		if err != nil {
			t.Fatal(err)
		}
		lockErr := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		closeErr := file.Close()
		if err := errors.Join(lockErr, closeErr); err != nil {
			t.Fatalf("directory lock not released %s: %v", parent, err)
		}
	}
}

func pipelineStoreFree(t *testing.T, f *pipelineFixture) {
	t.Helper()
	path := pipelineDBPath(t, f)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := tm.Open(t.Context(), path, f.limits)
	if err != nil {
		t.Fatalf("Store lock not released: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func pipelineDBPath(t *testing.T, f *pipelineFixture) string {
	t.Helper()
	var number int
	var name, path string
	if err := f.db.QueryRow("PRAGMA database_list").Scan(&number, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}

func pipelineOtherMount(t *testing.T, from string) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "yakuori-issue21-task5-")
	if err != nil {
		t.Fatal(err)
	}
	// Registration precedes any use of this additional child root.
	pipelineJSON(t, "owned-child-root.json", root)
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
		pipelineJSON(t, "child-root-cleanup.json", pipelineImage(t, root))
	})
	a, b := pipelineImage(t, from), pipelineImage(t, root)
	if a.ID.Mount == b.ID.Mount {
		t.Fatalf("distinct mount required: %s=%+v %s=%+v", from, a.ID, root, b.ID)
	}
	pipelineJSON(t, "mounts.json", []pipelineFileImage{a, b})
	t.Logf("real mounts destination=%d other=%d uid=%d", a.ID.Mount, b.ID.Mount, os.Geteuid())
	return root
}

func TestPipelineFilesystem(t *testing.T) {
	t.Run("source-across-mount", func(t *testing.T) {
		for _, kind := range []string{"text", "w3"} {
			for _, mode := range pipelineFileModes[:3] {
				t.Run(kind+"/"+mode.name, func(t *testing.T) {
					// Given: a real source on a different mount, qualified output.
					f := pipeline72NewFixture(t, kind)
					options := pipelineOptions(t, f, mode.name)
					localSource := pipelineImage(t, options.Source)
					other := pipelineOtherMount(t, filepath.Dir(options.Output))
					options.Source = filepath.Join(other, "source")
					writeFile(t, options.Source, f.input.source)
					source := pipelineImage(t, options.Source)
					want := f.expectedRows(t, f.input.units)
					pipelineJSON(t, "expected-rows.json", want)
					// When: ordinary real FileWithTM publishes without a rename redirect.
					result, err := f.core.FileWithTM(t.Context(), options, f.adapter, f.profile, f.store, f.scope)
					// Then: fully accepted translation, source inode/bytes and D1.
					if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
						t.Fatalf("result=%+v error=%v", result, err)
					}
					pipelineEqual(t, pipelineImage(t, options.Source), source)
					pipelineEqual(t, pipelineImage(t, localSource.Path), localSource)
					pipelineWantTranslated(t, f, options.Output)
					pipelineEqual(t, pipelineFixtureReadRows(t, f.db), want)
					f.record = result.Publication.RecordPath
					f.stage = filepath.Join(filepath.Dir(f.record), "stage")
					pipelineEqual(t, pipelineImage(t, f.stage).Present, false)
					pipelineBoundaryState(t, f, options, result, err)
					pipelineLocksFree(t, filepath.Dir(options.Source), filepath.Dir(options.Output))
					pipelineStoreFree(t, f)
				})
			}
		}
	})
	t.Run("path-refusals", testPipelinePathRefusals)
	t.Run("drift", testPipelineFilesystemDrift)
	t.Run("unvalidated-stage", func(t *testing.T) {
		for _, kind := range []string{"text", "w3"} {
			t.Run(kind, func(t *testing.T) {
				// Given: stage is changed on disk before the real Observe acceptance.
				f := pipeline72NewFixture(t, kind)
				options := pipelineOptions(t, f, "replace-existing")
				before := pipelineImages(t, []string{options.Source, options.Output})
				reached := 0
				hooks := publication.PipelineFaultHooks{CloseStage: func(file *os.File) error {
					reached++
					writeFile(t, f.stage, []byte("Unvalidated stage X.\n"))
					return nil
				}}
				// When: the real closed-stage check reads the altered bytes.
				result, err := pipeline72Execute(f, t.Context(), options, hooks)
				// Then: no accepted new TM row, no publication, retained X.
				if err == nil || result.TMCommitted || result.Publication.State != publication.NotPublished || reached != 1 {
					t.Fatalf("result=%+v error=%v reached=%d", result, err, reached)
				}
				pipelineEqual(t, pipelineImages(t, []string{options.Source, options.Output}), before)
				pipelineEqual(t, fileBytes(t, f.stage), []byte("Unvalidated stage X.\n"))
				pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.d0)
				pipelineJSON(t, "expected-rows.json", f.d0)
				pipelineBoundaryState(t, f, options, result, err)
				pipelineLocksFree(t, filepath.Dir(options.Output))
				pipelineStoreFree(t, f)
			})
		}
	})
	t.Run("physical-errno", testPipelinePhysicalErrno)
}

func testPipelinePhysicalErrno(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Fatal("required physical EACCES tests must execute as non-root")
	}
	for _, kind := range []string{"text", "w3"} {
		for _, mode := range pipelineFileModes {
			cases := []string{"publish-eacces", "publish-exdev"}
			if mode.mode == publication.InPlace {
				cases = append(cases, "backup-eacces", "restore-eacces")
			}
			for _, name := range cases {
				t.Run(kind+"/"+mode.name+"/"+name, func(t *testing.T) {
					// Given: completed validation/commit and a real destination FD.
					f := pipeline72NewFixture(t, kind)
					options := pipelineOptions(t, f, mode.name)
					original := pipelineImage(t, options.Source)
					oldOutput := pipelineImage(t, options.Output)
					parent := filepath.Dir(options.Output)
					parentBefore := pipelineImage(t, parent)
					parentMode := parentBefore.ID.Mode & 07777
					pipelineJSON(t, "parent-before.json", parentBefore)
					t.Cleanup(func() {
						if err := os.Chmod(parent, os.FileMode(parentMode)); err != nil {
							t.Error(err)
						}
					})
					var other *os.File
					var external pipelineFileImage
					if name == "publish-exdev" {
						root := pipelineOtherMount(t, parent)
						writeFile(t, filepath.Join(root, "output"), []byte("Owned external X.\n"))
						external = pipelineImage(t, filepath.Join(root, "output"))
						pipelineJSON(t, "external-before.json", external)
						var err error
						other, err = os.Open(root)
						if err != nil {
							t.Fatal(err)
						}
						defer func() {
							if err := other.Close(); err != nil {
								t.Error(err)
							}
						}()
					}
					reached := make(map[string]int)
					var physical []string
					var expectedRecord pipelineOperationRecord
					var expectedRecordBytes []byte
					var retainedRecord pipelineFileImage
					phase, operation := "prepared", "none"
					hooks := publication.PipelineFaultHooks{Rename: func(a int, from string, b int, to string, flags uint) error {
						if to == "record.json" {
							err := unix.Renameat2(a, from, b, to, flags)
							if err == nil {
								retainedRecord = pipelineImage(t, f.record)
								if !retainedRecord.Present {
									t.Fatal("successful record install did not retain the operation record")
								}
								pipelineEqual(t, retainedRecord.Bytes, expectedRecordBytes)
								pipelineJSON(t, "expected-retained-record.json", retainedRecord)
							}
							return err
						}
						publish := from == "stage"
						backup := f.backup != "" && to == filepath.Base(f.backup)
						restore := f.backup != "" && from == filepath.Base(f.backup)
						if publish || backup || restore {
							pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.expectedRows(t, f.input.units))
						}
						if publish && name == "publish-exdev" {
							reached["redirected-publish"]++
							err := unix.Renameat2(a, from, int(other.Fd()), "output", flags)
							physical = append(physical, fmt.Sprintf("redirected publish flags=%d errno=%v", flags, err))
							return err
						}
						deny := (publish && name == "publish-eacces") || (backup && name == "backup-eacces") ||
							((publish || restore) && name == "restore-eacces")
						if deny {
							reached["permission-denied"]++
							if err := unix.Fchmod(b, 0500); err != nil {
								return err
							}
							err := unix.Renameat2(a, from, b, to, flags)
							physical = append(physical, fmt.Sprintf("from=%s to=%s flags=%d errno=%v uid=%d", from, to, flags, err, os.Geteuid()))
							if !errors.Is(err, unix.EACCES) {
								t.Fatalf("physical permission operation did not return EACCES: %v", err)
							}
							// Restore the original parent mode before recovery's identity
							// recheck. The restore-denied row chmods again at its real syscall.
							return errors.Join(err, unix.Fchmod(b, uint32(parentMode)))
						}
						if restore {
							reached["restore-allowed"]++
						}
						err := unix.Renameat2(a, from, b, to, flags)
						if backup || restore {
							physical = append(physical, fmt.Sprintf("from=%s to=%s flags=%d errno=%v uid=%d", from, to, flags, err, os.Geteuid()))
						}
						if err == nil {
							switch {
							case backup:
								phase, operation = "source-backed-up", "backup-rename"
							case restore:
								phase, operation = "restored", "restore-rename"
							}
						}
						return err
					}}
					hooks.RecordWrite = func(file *os.File, raw []byte) (int, error) {
						// Build all expected fields from authored S/O, observed validated T
						// and the independently reached operation, never the saved record.
						expectedRecord = pipelineOperationRecord{
							SchemaVersion: 1, RunID: strings.TrimPrefix(filepath.Base(filepath.Dir(f.record)), ".yakuori-run-"),
							Mode: options.Mode, Source: pipelineRecordedFile(original),
							Output:       pipelineRecordPath{options.Output, filepath.Base(options.Output)},
							Stage:        pipelineRecordedFile(pipelineImage(t, f.stage)),
							SourceParent: pipelineRecordedID(parentBefore), OutputParent: pipelineRecordedID(parentBefore),
							Phase: phase, LastCompletedOperation: operation,
						}
						expectedRecord.ExistingOutput.Absent = !oldOutput.Present
						if oldOutput.Present {
							output := pipelineRecordedFile(oldOutput)
							expectedRecord.ExistingOutput.File = &output
						}
						if options.Mode == publication.InPlace {
							expectedRecord.Backup = &pipelineRecordPath{f.backup, filepath.Base(f.backup)}
						}
						var err error
						expectedRecordBytes, err = json.Marshal(expectedRecord)
						if err != nil {
							t.Fatal(err)
						}
						pipelineJSON(t, "expected-record-fields.json", expectedRecord)
						pipelineFixtureSave(t, "supplied-record.json", raw)
						switch phase {
						case "restored":
							if !strings.HasPrefix(file.Name(), ".record-recovery-") {
								t.Errorf("record filename=%q expected recovery snapshot after successful restore", file.Name())
							}
						default:
							if file.Name() != ".record-"+phase {
								t.Errorf("record filename=%q expected=%q after successful operations", file.Name(), ".record-"+phase)
							}
						}
						pipelineEqual(t, raw, expectedRecordBytes)
						return file.Write(raw)
					}
					// When: actual renameat2 encounters chmod EACCES or redirected EXDEV.
					var prepared *publication.Run
					runFD := -1
					result, err := f.core.fileWithTM(t.Context(), options, f.adapter, f.profile, nil,
						&tmExecution{store: f.store, scope: f.scope}, func(ctx context.Context, options publication.Options) (publicationRun, error) {
							run, err := publication.PrepareWithLimits(ctx, options, f.limits)
							if err != nil {
								return nil, err
							}
							prepared = run
							pipeline72SetPaths(f, run.SetPipelineFaultHooks(hooks))
							runFD = pipelineRunFD(t, filepath.Dir(f.stage))
							return run, nil
						})
					if prepared == nil {
						t.Fatalf("physical-error Run was not prepared: %v", err)
					}
					pipelineRunClosed(t, prepared, runFD) // Before capture, child exit or manual teardown.
					if !retainedRecord.Present {
						t.Fatal("required operation record was never observed installed")
					}
					if actual := pipelineImage(t, f.record); !bytes.Equal(actual.Bytes, expectedRecordBytes) || actual.ID != retainedRecord.ID || !actual.Present {
						t.Fatalf("retained operation record lost or changed at Core return: actual=%+v expected=%+v", actual, retainedRecord)
					}
					pipelineBoundaryState(t, f, options, result, err)
					pipelineJSON(t, "operations.json", physical)
					if external.Path != "" {
						pipelineJSON(t, "external-after.json", pipelineImage(t, external.Path))
					}
					t.Logf("reached=%v operations=%v result=%+v error=%v", reached, physical, result, err)
					// Then: original/external bytes and identities are never replaced.
					wantErr, wantState := error(unix.EACCES), publication.NotPublished
					if name == "publish-exdev" {
						wantErr = unix.EXDEV
						pipelineEqual(t, reached["redirected-publish"], 1)
						pipelineEqual(t, pipelineImage(t, external.Path), external)
					} else {
						wantDenials := 1
						if name == "restore-eacces" {
							wantDenials = 2
						}
						pipelineEqual(t, reached["permission-denied"], wantDenials)
					}
					if mode.mode == publication.InPlace && name != "backup-eacces" {
						wantState = publication.Restored
						if name == "restore-eacces" {
							wantState = publication.SourceBackedUp
						} else {
							pipelineEqual(t, reached["restore-allowed"], 1)
						}
					}
					if !errors.Is(err, wantErr) || !result.TMCommitted || result.Publication.State != wantState {
						t.Fatalf("result=%+v error=%v reached=%v", result, err, reached)
					}
					var recovery *publication.RecoveryError
					if wantState == publication.SourceBackedUp {
						if !errors.As(err, &recovery) {
							t.Fatalf("missing typed recovery error: %v", err)
						}
						pipelineEqual(t, pipelineImage(t, options.Source).Present, false)
						backup := pipelineImage(t, f.backup)
						pipelineEqual(t, backup.ID, original.ID)
						pipelineEqual(t, backup.Bytes, original.Bytes)
					} else {
						pipelineEqual(t, pipelineImage(t, options.Source), original)
						if mode.mode == publication.InPlace {
							pipelineEqual(t, pipelineImage(t, f.backup).Present, false)
						} else {
							pipelineEqual(t, pipelineImage(t, options.Output), oldOutput)
						}
					}
					pipelineWantTranslated(t, f, f.stage)
					pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.expectedRows(t, f.input.units))
					pipelineJSON(t, "expected-rows.json", f.expectedRows(t, f.input.units))
					pipelineLocksFree(t, parent)
					t.Logf("physical, not injected errno: %v; result=%s; EXDEV uses redirected destination FD, not natural stage placement", physical, wantState)
					oldStage := pipelineImage(t, f.stage)
					rerun := pipelinePublicRerun(t, f, options)
					if rerun.Error != "" || rerun.Result == nil || !rerun.Result.TMCommitted ||
						rerun.Result.Publication.State != publication.Published || rerun.Generated != 0 {
						t.Fatalf("valid retained record did not recover and publish from TM in new PID: %+v", rerun)
					}
					if rerun.Result.Publication.RunID == result.Publication.RunID {
						t.Fatal("public rerun reused the old publication run")
					}
					pipelineWantTranslated(t, f, options.Output)
					pipelineEqual(t, pipelineImage(t, f.stage), oldStage)
					if pipelineImage(t, options.Output).ID.Inode == oldStage.ID.Inode {
						t.Fatal("public rerun published the old retained stage")
					}
					if options.Mode == publication.InPlace {
						backup := pipelineImage(t, rerun.Result.Publication.BackupPath)
						pipelineEqual(t, backup.ID, original.ID)
						pipelineEqual(t, backup.Bytes, original.Bytes)
						pipelineEqual(t, pipelineImage(t, f.backup).Present, false)
					} else {
						pipelineEqual(t, pipelineImage(t, options.Source), original)
					}
					if external.Path != "" {
						pipelineEqual(t, pipelineImage(t, external.Path), external)
					}
					switch wantState {
					case publication.NotPublished:
						expectedRecord.Phase, expectedRecord.LastCompletedOperation = "abandoned", "reconcile-unpublished"
					case publication.SourceBackedUp:
						expectedRecord.Phase, expectedRecord.LastCompletedOperation = "restored", "restore-rename"
					case publication.Restored:
						pipelineEqual(t, pipelineImage(t, f.record), retainedRecord)
					}
					expectedAfterRecovery, marshalErr := json.Marshal(expectedRecord)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					pipelineEqual(t, fileBytes(t, f.record), expectedAfterRecovery)
					pipelineJSON(t, "rerun-old-assets.json", pipelineImages(t, []string{f.record, f.stage, f.backup}))
					pipelineJSON(t, "rerun-new-assets.json", pipelineImages(t, []string{options.Source, options.Output, rerun.Paths.Record, rerun.Paths.Backup}))
					pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.expectedRows(t, f.input.units))
					pipelineLocksFree(t, parent)
					pipelineStoreFree(t, f)
				})
			}
		}
	}
}

func TestPipelineNoClobber(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		for _, name := range []string{"create-arrival", "replace-absent-arrival", "in-place-publish-arrival",
			"after-backup-error", "after-backup-cancel", "restore-arrival", "backup-existing", "backup-generated"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				// Given: real Core/TM, owned S and T, independently authored external X.
				f := pipeline72NewFixture(t, kind)
				mode := "in-place"
				switch name {
				case "create-arrival":
					mode = "create"
				case "replace-absent-arrival":
					mode = "replace-absent"
				}
				options := pipelineOptions(t, f, mode)
				source := pipelineImage(t, options.Source)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var external pipelineFileImage
				var reached []string
				var nativeErr error
				arrive := func(path string) {
					t.Helper()
					file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
					if err != nil {
						t.Fatal(err)
					}
					_, writeErr := file.Write([]byte("External X.\n"))
					if err := errors.Join(writeErr, file.Close()); err != nil {
						t.Fatal(err)
					}
					external = pipelineImage(t, path)
				}
				hooks := publication.PipelineFaultHooks{}
				hooks.RecordWrite = func(file *os.File, raw []byte) (int, error) {
					if name == "backup-generated" && file.Name() == ".record-prepared" {
						reached = append(reached, "after-backupAvailable-before-backup-rename")
						arrive(f.backup)
					}
					return file.Write(raw)
				}
				hooks.SyncStage = func(file *os.File) error {
					if name == "backup-existing" {
						reached = append(reached, "stage-sync-before-backupAvailable")
						arrive(f.backup)
					}
					return file.Sync()
				}
				hooks.Rename = func(a int, from string, b int, to string, flags uint) error {
					if from == "stage" {
						reached = append(reached, "publish-rename")
						pipelineWantTranslated(t, f, f.stage)
						switch name {
						case "create-arrival", "replace-absent-arrival", "in-place-publish-arrival":
							arrive(options.Output)
							if flags != unix.RENAME_NOREPLACE {
								t.Fatalf("arrival must be protected by real NOREPLACE, flags=%d", flags)
							}
							nativeErr = unix.Renameat2(a, from, b, to, flags)
							return nativeErr
						case "restore-arrival":
							return unix.EIO // Explicit injected publish error, not physical EIO.
						}
					}
					if name == "restore-arrival" && from == filepath.Base(f.backup) {
						reached = append(reached, "restore-rename")
						arrive(options.Source)
						pipelineEqual(t, flags, uint(unix.RENAME_NOREPLACE))
						nativeErr = unix.Renameat2(a, from, b, to, flags)
						return nativeErr
					}
					if name == "backup-generated" && to == filepath.Base(f.backup) {
						reached = append(reached, "backup-rename")
						pipelineEqual(t, flags, uint(unix.RENAME_NOREPLACE))
						nativeErr = unix.Renameat2(a, from, b, to, flags)
						return nativeErr
					}
					err := unix.Renameat2(a, from, b, to, flags)
					if err == nil && from == ".record-source-backed-up" &&
						(name == "after-backup-error" || name == "after-backup-cancel") {
						reached = append(reached, "after-backup-before-final-revalidate")
						arrive(options.Source)
						if name == "after-backup-cancel" {
							cancel()
						}
					}
					return err
				}
				pipelineJSON(t, "expected-rows.json", f.expectedRows(t, f.input.units))
				// When: the external file arrives at the named actual boundary.
				result, err := pipeline72Execute(f, ctx, options, hooks)
				// Then: no-clobber preserves X's inode/bytes and S or its original backup.
				wantState, wantErr := publication.NotPublished, error(unix.EEXIST)
				switch name {
				case "in-place-publish-arrival", "after-backup-error", "after-backup-cancel", "restore-arrival":
					wantState = publication.SourceBackedUp
					var recovery *publication.RecoveryError
					if !errors.As(err, &recovery) || !errors.Is(err, publication.ErrRecoveryConflict) {
						t.Fatalf("missing recovery conflict: %v", err)
					}
					backup := pipelineImage(t, f.backup)
					pipelineEqual(t, backup.ID, source.ID)
					pipelineEqual(t, backup.Bytes, source.Bytes)
				}
				switch name {
				case "after-backup-error":
					wantErr = publication.ErrChanged
				case "after-backup-cancel":
					wantErr = context.Canceled
				case "restore-arrival":
					wantErr = unix.EIO
				}
				if !errors.Is(err, wantErr) || result.Publication.State != wantState || !result.TMCommitted || len(reached) == 0 {
					t.Fatalf("result=%+v error=%v checkpoints=%v", result, err, reached)
				}
				if name != "backup-existing" && !strings.HasPrefix(name, "after-backup") && !errors.Is(nativeErr, unix.EEXIST) {
					t.Fatalf("actual NOREPLACE EEXIST not observed: %v", nativeErr)
				}
				pipelineEqual(t, pipelineImage(t, external.Path), external)
				if wantState == publication.NotPublished {
					pipelineEqual(t, pipelineImage(t, options.Source), source)
				}
				pipelineWantTranslated(t, f, f.stage)
				pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.expectedRows(t, f.input.units))
				pipelineJSON(t, "external.json", external)
				pipelineJSON(t, "operations.json", struct {
					Checkpoints   []string
					PhysicalErrno string
				}{reached, fmt.Sprint(nativeErr)})
				pipelineBoundaryState(t, f, options, result, err)
				pipelineLocksFree(t, filepath.Dir(options.Source))
				pipelineStoreFree(t, f)
				t.Logf("checkpoints=%v physical rename errno=%v; stimulus=%s result=%s", reached, nativeErr, name, wantState)
				preservedPaths := []string{options.Source, options.Output, f.stage, f.record, f.backup, external.Path}
				beforeRerun := pipelineImages(t, preservedPaths)
				rerun := pipelinePublicRerun(t, f, options)
				wantClass := "ErrRecoveryConflict"
				if name == "backup-existing" {
					wantClass = "ErrInvalidRecord" // Collision occurred before any record was written.
				}
				if !rerun.Recovery || rerun.Phase != "prepare" || rerun.Class != wantClass ||
					rerun.Result == nil || rerun.Result.TMCommitted || rerun.Result.Publication.State != publication.NotPublished || rerun.Generated != 0 {
					t.Fatalf("public rerun did not refuse retained conflict without generation: %+v expected=%s", rerun, wantClass)
				}
				pipelineEqual(t, pipelineImages(t, preservedPaths), beforeRerun)
				pipelineJSON(t, "rerun-preserved-assets.json", beforeRerun)
				pipelineLocksFree(t, filepath.Dir(options.Source))
				pipelineStoreFree(t, f)
			})
		}
	}
}

func testPipelinePathRefusals(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		for _, name := range []string{"source-symlink", "output-symlink", "dangling-source", "dangling-output",
			"source-component", "output-component", "source-dotdot", "output-dotdot", "source-hardlink", "output-hardlink",
			"same-path", "dot-alias", "in-place-different", "in-place-symlink", "directory-source", "directory-output", "fifo-source", "fifo-output", "existing-create"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				// Given: an unsafe real leaf/path and preserved independent originals.
				f := pipeline72NewFixture(t, kind)
				options := pipelineOptions(t, f, "replace-existing")
				source, output := options.Source, options.Output
				parent := filepath.Dir(source)
				alias := filepath.Join(parent, "alias")
				want := error(publication.ErrInvalidPath)
				switch name {
				case "source-symlink", "output-symlink", "in-place-symlink":
					target := source
					if name == "output-symlink" {
						target = output
					}
					if err := os.Symlink(target, alias); err != nil {
						t.Fatal(err)
					}
					if name == "source-symlink" {
						options.Source = alias
					} else {
						options.Output = alias
					}
					if name == "in-place-symlink" {
						options.Mode = publication.InPlace
						want = publication.ErrAlias
					}
				case "dangling-source", "dangling-output":
					if err := os.Symlink(filepath.Join(parent, "absent"), alias); err != nil {
						t.Fatal(err)
					}
					if name == "dangling-source" {
						options.Source = alias
					} else {
						options.Output = alias
					}
				case "source-component", "output-component", "source-dotdot", "output-dotdot":
					if err := os.Symlink(parent, alias); err != nil {
						t.Fatal(err)
					}
					path := alias + "/source"
					if strings.Contains(name, "dotdot") {
						path = alias + "/../" + filepath.Base(parent) + "/source"
					}
					if strings.HasPrefix(name, "source") {
						options.Source = path
					} else {
						options.Output = path
					}
				case "source-hardlink", "output-hardlink":
					target := source
					if name == "output-hardlink" {
						target = output
					}
					if err := os.Link(target, alias); err != nil {
						t.Fatal(err)
					}
				case "same-path", "dot-alias":
					options.Output, want = source, publication.ErrAlias
					if name == "dot-alias" {
						options.Output = parent + "/./source"
					}
				case "in-place-different":
					options.Mode, want = publication.InPlace, publication.ErrAlias
				case "directory-source", "directory-output", "fifo-source", "fifo-output":
					var err error
					if strings.HasPrefix(name, "directory") {
						err = os.Mkdir(alias, 0700)
					} else {
						err = unix.Mkfifo(alias, 0600)
					}
					if err != nil {
						t.Fatal(err)
					}
					if strings.HasSuffix(name, "source") {
						options.Source = alias
					} else {
						options.Output = alias
					}
				case "existing-create":
					options.Mode, want = publication.Create, os.ErrExist
				}
				paths := []string{source, output, alias}
				before := pipelineImages(t, paths)
				entries, err := os.ReadDir(parent)
				if err != nil {
					t.Fatal(err)
				}
				// When: real Core preflight examines the authored unsafe request.
				result, err := pipeline72Execute(f, t.Context(), options, publication.PipelineFaultHooks{})
				// Then: exact typed refusal before generation/run creation and D0.
				var phase *Error
				if !errors.Is(err, want) || !errors.As(err, &phase) || phase.Phase != "prepare" ||
					result.TMCommitted || result.Publication.State != publication.NotPublished || len(f.backend.calls) != 0 {
					t.Fatalf("result=%+v error=%v want=%v Generate=%d", result, err, want, len(f.backend.calls))
				}
				afterEntries, readErr := os.ReadDir(parent)
				if readErr != nil {
					t.Fatal(readErr)
				}
				pipelineEqual(t, len(afterEntries), len(entries))
				actual := pipelineImages(t, paths)
				pipelineEqual(t, actual, before)
				pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.d0)
				pipelineJSON(t, "preserved.json", before)
				pipelineJSON(t, "actual-files.json", actual)
				pipelineJSON(t, "expected-rows.json", f.d0)
				pipelineBoundaryState(t, f, options, result, err)
				pipelineJSON(t, "result.json", struct {
					Error  string
					Result Result
				}{err.Error(), result})
				pipelineLocksFree(t, parent)
				pipelineStoreFree(t, f)
			})
		}
		for _, spelling := range []string{"exact", "dot"} {
			t.Run(kind+"/explicit-same-path/"+spelling, func(t *testing.T) {
				// Given: explicit InPlace, not an ordinary-output alias request.
				f := pipeline72NewFixture(t, kind)
				options := pipelineOptions(t, f, "in-place")
				if spelling == "dot" {
					options.Output = filepath.Dir(options.Source) + "/./source"
				}
				source := pipelineImage(t, options.Source)
				// When: execute the explicit same validated parent/basename.
				result, err := pipeline72Execute(f, t.Context(), options, publication.PipelineFaultHooks{})
				// Then: stage replaces source and the original inode survives as backup.
				if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				backup := pipelineImage(t, result.Publication.BackupPath)
				pipelineEqual(t, backup.ID, source.ID)
				pipelineEqual(t, backup.Bytes, source.Bytes)
				pipelineWantTranslated(t, f, options.Output)
				pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.expectedRows(t, f.input.units))
				pipelineBoundaryState(t, f, options, result, err)
				pipelineLocksFree(t, filepath.Dir(options.Source))
				pipelineStoreFree(t, f)
			})
		}
	}
}

func testPipelineFilesystemDrift(t *testing.T) {
	for _, kind := range []string{"text", "w3"} {
		for _, mode := range pipelineFileModes {
			cases := []string{"source-bytes", "source-inode", "source-link", "source-mode",
				"source-symlink", "source-absent", "stage-bytes", "stage-inode", "stage-link", "stage-mode",
				"parent-inode", "parent-mode", "run-inode", "run-mode", "dotdot-component"}
			if mode.mode != publication.InPlace {
				cases = append(cases, "source-parent-inode", "source-parent-mode", "output-parent-inode", "output-parent-mode")
			}
			if mode.name == "replace-existing" {
				cases = append(cases, "output-bytes", "output-inode", "output-link", "output-mode", "output-alias")
			} else if mode.mode != publication.InPlace {
				cases = append(cases, "output-arrival")
			}
			for _, name := range cases {
				t.Run(kind+"/"+mode.name+"/"+name, func(t *testing.T) {
					// Given: a validated T and confirmed D1, then observed drift X.
					f := pipeline72NewFixture(t, kind)
					options := pipelineOptions(t, f, mode.name)
					if strings.HasPrefix(name, "source-parent") || strings.HasPrefix(name, "output-parent") {
						options.Source = filepath.Join(t.TempDir(), "source")
						writeFile(t, options.Source, f.input.source)
					}
					parent := filepath.Dir(options.Source)
					if name == "dotdot-component" {
						if err := os.Mkdir(filepath.Join(parent, "walk"), 0700); err != nil {
							t.Fatal(err)
						}
						options.Source = parent + "/walk/../source"
					}
					paths := []string{filepath.Join(parent, "source"), options.Output}
					var checkpoint []pipelineFileImage
					reached := 0
					hooks := publication.PipelineFaultHooks{Rename: func(a int, from string, b int, to string, flags uint) error {
						err := unix.Renameat2(a, from, b, to, flags)
						if err != nil || from != ".record-prepared" {
							return err
						}
						reached++
						pipelineWantTranslated(t, f, f.stage)
						pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.expectedRows(t, f.input.units))
						paths = append(paths, f.stage, f.record, f.backup)
						target := options.Source
						if strings.HasPrefix(name, "stage") {
							target = f.stage
						} else if strings.HasPrefix(name, "output") {
							target = options.Output
						}
						rename := func(from, to string) {
							t.Helper()
							if err := os.Rename(from, to); err != nil {
								t.Fatal(err)
							}
							paths = append(paths, to)
						}
						switch name {
						case "source-bytes", "output-bytes", "stage-bytes", "output-arrival":
							writeFile(t, target, []byte("External X.\n"))
						case "source-inode", "output-inode", "stage-inode":
							raw := fileBytes(t, target)
							rename(target, target+"-saved")
							writeFile(t, target, raw) // Same bytes; only inode changed.
						case "source-link", "output-link", "stage-link":
							if err := os.Link(target, target+"-alias"); err != nil {
								t.Fatal(err)
							}
							paths = append(paths, target+"-alias")
						case "source-mode", "output-mode", "stage-mode":
							if err := os.Chmod(target, 0400); err != nil {
								t.Fatal(err)
							}
						case "source-symlink", "source-absent":
							rename(target, target+"-saved")
							if name == "source-symlink" {
								if err := os.Symlink(target+"-saved", target); err != nil {
									t.Fatal(err)
								}
							}
						case "output-alias":
							rename(target, target+"-saved")
							if err := os.Link(options.Source, target); err != nil {
								t.Fatal(err)
							}
						case "parent-inode", "source-parent-inode", "output-parent-inode":
							driftParent := parent
							if name == "output-parent-inode" {
								driftParent = filepath.Dir(options.Output)
							}
							moved := driftParent + "-moved"
							rename(driftParent, moved)
							t.Cleanup(func() {
								if err := os.RemoveAll(moved); err != nil {
									t.Error(err)
								}
							})
							if err := os.Mkdir(driftParent, 0700); err != nil {
								t.Fatal(err)
							}
							oldPaths := slices.Clone(paths)
							paths = paths[:0]
							for _, path := range oldPaths {
								if strings.HasPrefix(path, driftParent+"/") {
									paths = append(paths, moved+strings.TrimPrefix(path, driftParent))
								} else {
									paths = append(paths, path)
								}
							}
							if strings.HasPrefix(f.stage, driftParent+"/") {
								f.stage = moved + strings.TrimPrefix(f.stage, driftParent)
								f.record = moved + strings.TrimPrefix(f.record, driftParent)
							}
							if driftParent == parent {
								writeFile(t, filepath.Join(parent, "source"), []byte("External source X.\n"))
								paths = append(paths, filepath.Join(parent, "source"))
							}
							if filepath.Dir(options.Output) == driftParent && options.Output != options.Source {
								writeFile(t, options.Output, []byte("External output X.\n"))
								paths = append(paths, options.Output)
							}
						case "parent-mode", "source-parent-mode", "output-parent-mode":
							driftParent := parent
							if name == "output-parent-mode" {
								driftParent = filepath.Dir(options.Output)
							}
							if err := os.Chmod(driftParent, 0750); err != nil {
								t.Fatal(err)
							}
						case "run-inode":
							run := filepath.Dir(f.stage)
							moved := run + "-moved"
							rename(run, moved)
							if err := os.Mkdir(run, 0700); err != nil {
								t.Fatal(err)
							}
							f.stage, f.record = filepath.Join(moved, "stage"), filepath.Join(moved, "record.json")
							paths = append(paths, f.stage, f.record)
						case "run-mode":
							if err := os.Chmod(filepath.Dir(f.stage), 0750); err != nil {
								t.Fatal(err)
							}
						case "dotdot-component":
							walk := filepath.Join(parent, "walk")
							rename(walk, walk+"-saved")
							if err := os.Symlink(walk+"-saved", walk); err != nil {
								t.Fatal(err)
							}
							paths = append(paths, walk)
						}
						checkpoint = pipelineImages(t, paths)
						pipelineJSON(t, "checkpoint.json", checkpoint)
						return nil // The record rename actually succeeded.
					}}
					// When: the second real revalidate observes drift before file renames.
					result, err := pipeline72Execute(f, t.Context(), options, hooks)
					// Then: all X/original/stage/record assets survive exactly, D1 stays T.
					if !errors.Is(err, publication.ErrChanged) || !result.TMCommitted ||
						result.Publication.State != publication.NotPublished || reached != 1 {
						t.Fatalf("result=%+v error=%v reached=%d", result, err, reached)
					}
					actual := pipelineImages(t, paths)
					pipelineEqual(t, actual, checkpoint)
					pipelineJSON(t, "actual-files.json", actual)
					pipelineEqual(t, pipelineFixtureReadRows(t, f.db), f.expectedRows(t, f.input.units))
					pipelineJSON(t, "expected-rows.json", f.expectedRows(t, f.input.units))
					pipelineBoundaryState(t, f, options, result, err)
					lockedParents := map[string]bool{}
					for _, path := range paths {
						if path != "" && !lockedParents[filepath.Dir(path)] {
							pipelineLocksFree(t, filepath.Dir(path))
							lockedParents[filepath.Dir(path)] = true
						}
					}
					pipelineStoreFree(t, f)
					t.Log("reached prepared-record rename; real final revalidate refused; no Replace-existing last-instruction CAS claim")
				})
			}
		}
	}
}
