//go:build linux && pipelinefaults

package localize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/artifact"
	"github.com/sh4869221b/yakuori/internal/publication"
	"github.com/sh4869221b/yakuori/internal/tm"
	"golang.org/x/sys/unix"
)

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
	pipelineSave(t, name, raw)
}

func pipelineBoundaryState(t *testing.T, f *pipelineFixture, options publication.Options, result Result, err error) {
	t.Helper()
	message := ""
	if err != nil {
		message = err.Error()
	}
	paths := []string{options.Source, options.Output, f.paths.Stage, f.paths.Record, f.paths.Backup}
	images := pipelineImages(t, paths)
	for i, image := range images {
		if image.Present && image.ID.Mode&unix.S_IFMT == unix.S_IFREG {
			pipelineSave(t, fmt.Sprintf("actual-%d.bin", i), image.Bytes)
		}
	}
	pipelineJSON(t, "state.json", struct {
		PID, UID int
		Layer    string
		Options  publication.Options
		Result   Result
		Error    string
		Files    []pipelineFileImage
		Rows     []pipelineTMRow
	}{os.Getpid(), os.Geteuid(), "whole Core; real Adapter/SQLite; controlled common Engine", options, result, message, images, pipelineReadRows(t, f.db)})
}

func pipelineOptions(t *testing.T, f *pipelineFixture, name string) publication.Options {
	t.Helper()
	for _, mode := range pipelineFileModes {
		if mode.name == name {
			options := f.options(t, mode.mode)
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
	pipelineEqual(t, got, f.texts)
	// W3 retains the authored binary table order, while the independent CSV
	// expectations use a different order. Compare the complete multiset.
	gotKeys, wantKeys := make(map[artifact.KeyRelation]int), make(map[artifact.KeyRelation]int)
	for _, key := range observed.Keys {
		gotKeys[key]++
	}
	for _, key := range f.scope.Keys {
		wantKeys[key]++
	}
	pipelineEqual(t, gotKeys, wantKeys)
	pipelineSave(t, "validated-artifact.bin", raw)
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
					f := newPipelineFixture(t, kind)
					options := pipelineOptions(t, f, mode.name)
					localSource := pipelineImage(t, options.Source)
					other := pipelineOtherMount(t, filepath.Dir(options.Output))
					options.Source = filepath.Join(other, "source")
					writeFile(t, options.Source, f.raw)
					source := pipelineImage(t, options.Source)
					want := f.expectedRows(t)
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
					pipelineEqual(t, pipelineReadRows(t, f.db), want)
					f.paths.Record = result.Publication.RecordPath
					f.paths.Stage = filepath.Join(filepath.Dir(f.paths.Record), "stage")
					pipelineEqual(t, pipelineImage(t, f.paths.Stage).Present, false)
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
				f := newPipelineFixture(t, kind)
				options := pipelineOptions(t, f, "replace-existing")
				before := pipelineImages(t, []string{options.Source, options.Output})
				reached := 0
				hooks := publication.PipelineFaultHooks{CloseStage: func(file *os.File) error {
					reached++
					writeFile(t, f.paths.Stage, []byte("Unvalidated stage X.\n"))
					return nil
				}}
				// When: the real closed-stage check reads the altered bytes.
				result, err := f.execute(t.Context(), options, hooks)
				// Then: no accepted new TM row, no publication, retained X.
				if err == nil || result.TMCommitted || result.Publication.State != publication.NotPublished || reached != 1 {
					t.Fatalf("result=%+v error=%v reached=%d", result, err, reached)
				}
				pipelineEqual(t, pipelineImages(t, []string{options.Source, options.Output}), before)
				pipelineEqual(t, fileBytes(t, f.paths.Stage), []byte("Unvalidated stage X.\n"))
				pipelineEqual(t, pipelineReadRows(t, f.db), f.initial)
				pipelineJSON(t, "expected-rows.json", f.initial)
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
					f := newPipelineFixture(t, kind)
					options := pipelineOptions(t, f, mode.name)
					original := pipelineImage(t, options.Source)
					oldOutput := pipelineImage(t, options.Output)
					parent := filepath.Dir(options.Output)
					parentMode := pipelineImage(t, parent).ID.Mode & 07777
					pipelineJSON(t, "parent-before.json", pipelineImage(t, parent))
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
					hooks := publication.PipelineFaultHooks{Rename: func(a int, from string, b int, to string, flags uint) error {
						publish := from == "stage"
						backup := f.paths.Backup != "" && to == filepath.Base(f.paths.Backup)
						restore := f.paths.Backup != "" && from == filepath.Base(f.paths.Backup)
						if publish || backup || restore {
							pipelineEqual(t, pipelineReadRows(t, f.db), f.expectedRows(t))
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
						return unix.Renameat2(a, from, b, to, flags)
					}}
					// When: actual renameat2 encounters chmod EACCES or redirected EXDEV.
					result, err := f.execute(t.Context(), options, hooks)
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
						backup := pipelineImage(t, f.paths.Backup)
						pipelineEqual(t, backup.ID, original.ID)
						pipelineEqual(t, backup.Bytes, original.Bytes)
					} else {
						pipelineEqual(t, pipelineImage(t, options.Source), original)
						if mode.mode == publication.InPlace {
							pipelineEqual(t, pipelineImage(t, f.paths.Backup).Present, false)
						} else {
							pipelineEqual(t, pipelineImage(t, options.Output), oldOutput)
						}
					}
					pipelineWantTranslated(t, f, f.paths.Stage)
					pipelineEqual(t, pipelineReadRows(t, f.db), f.expectedRows(t))
					pipelineJSON(t, "expected-rows.json", f.expectedRows(t))
					pipelineLocksFree(t, parent)
					pipelineStoreFree(t, f)
					t.Logf("physical, not injected errno: %v; result=%s; EXDEV uses redirected destination FD, not natural stage placement", physical, wantState)
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
				f := newPipelineFixture(t, kind)
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
						arrive(f.paths.Backup)
					}
					return file.Write(raw)
				}
				hooks.SyncStage = func(file *os.File) error {
					if name == "backup-existing" {
						reached = append(reached, "stage-sync-before-backupAvailable")
						arrive(f.paths.Backup)
					}
					return file.Sync()
				}
				hooks.Rename = func(a int, from string, b int, to string, flags uint) error {
					if from == "stage" {
						reached = append(reached, "publish-rename")
						pipelineWantTranslated(t, f, f.paths.Stage)
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
					if name == "restore-arrival" && from == filepath.Base(f.paths.Backup) {
						reached = append(reached, "restore-rename")
						arrive(options.Source)
						pipelineEqual(t, flags, uint(unix.RENAME_NOREPLACE))
						nativeErr = unix.Renameat2(a, from, b, to, flags)
						return nativeErr
					}
					if name == "backup-generated" && to == filepath.Base(f.paths.Backup) {
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
				pipelineJSON(t, "expected-rows.json", f.expectedRows(t))
				// When: the external file arrives at the named actual boundary.
				result, err := f.execute(ctx, options, hooks)
				// Then: no-clobber preserves X's inode/bytes and S or its original backup.
				wantState, wantErr := publication.NotPublished, error(unix.EEXIST)
				switch name {
				case "in-place-publish-arrival", "after-backup-error", "after-backup-cancel", "restore-arrival":
					wantState = publication.SourceBackedUp
					var recovery *publication.RecoveryError
					if !errors.As(err, &recovery) || !errors.Is(err, publication.ErrRecoveryConflict) {
						t.Fatalf("missing recovery conflict: %v", err)
					}
					backup := pipelineImage(t, f.paths.Backup)
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
				pipelineWantTranslated(t, f, f.paths.Stage)
				pipelineEqual(t, pipelineReadRows(t, f.db), f.expectedRows(t))
				pipelineJSON(t, "external.json", external)
				pipelineJSON(t, "operations.json", struct {
					Checkpoints   []string
					PhysicalErrno string
				}{reached, fmt.Sprint(nativeErr)})
				pipelineBoundaryState(t, f, options, result, err)
				pipelineLocksFree(t, filepath.Dir(options.Source))
				pipelineStoreFree(t, f)
				t.Logf("checkpoints=%v physical rename errno=%v; stimulus=%s result=%s", reached, nativeErr, name, wantState)
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
				f := newPipelineFixture(t, kind)
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
				result, err := f.execute(t.Context(), options, publication.PipelineFaultHooks{})
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
				pipelineEqual(t, pipelineReadRows(t, f.db), f.initial)
				pipelineJSON(t, "preserved.json", before)
				pipelineJSON(t, "actual-files.json", actual)
				pipelineJSON(t, "expected-rows.json", f.initial)
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
				f := newPipelineFixture(t, kind)
				options := pipelineOptions(t, f, "in-place")
				if spelling == "dot" {
					options.Output = filepath.Dir(options.Source) + "/./source"
				}
				source := pipelineImage(t, options.Source)
				// When: execute the explicit same validated parent/basename.
				result, err := f.execute(t.Context(), options, publication.PipelineFaultHooks{})
				// Then: stage replaces source and the original inode survives as backup.
				if err != nil || !result.TMCommitted || result.Publication.State != publication.Published {
					t.Fatalf("result=%+v error=%v", result, err)
				}
				backup := pipelineImage(t, result.Publication.BackupPath)
				pipelineEqual(t, backup.ID, source.ID)
				pipelineEqual(t, backup.Bytes, source.Bytes)
				pipelineWantTranslated(t, f, options.Output)
				pipelineEqual(t, pipelineReadRows(t, f.db), f.expectedRows(t))
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
					f := newPipelineFixture(t, kind)
					options := pipelineOptions(t, f, mode.name)
					if strings.HasPrefix(name, "source-parent") || strings.HasPrefix(name, "output-parent") {
						options.Source = filepath.Join(t.TempDir(), "source")
						writeFile(t, options.Source, f.raw)
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
						pipelineWantTranslated(t, f, f.paths.Stage)
						pipelineEqual(t, pipelineReadRows(t, f.db), f.expectedRows(t))
						paths = append(paths, f.paths.Stage, f.paths.Record, f.paths.Backup)
						target := options.Source
						if strings.HasPrefix(name, "stage") {
							target = f.paths.Stage
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
							if strings.HasPrefix(f.paths.Stage, driftParent+"/") {
								f.paths.Stage = moved + strings.TrimPrefix(f.paths.Stage, driftParent)
								f.paths.Record = moved + strings.TrimPrefix(f.paths.Record, driftParent)
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
							run := filepath.Dir(f.paths.Stage)
							moved := run + "-moved"
							rename(run, moved)
							if err := os.Mkdir(run, 0700); err != nil {
								t.Fatal(err)
							}
							f.paths.Stage, f.paths.Record = filepath.Join(moved, "stage"), filepath.Join(moved, "record.json")
							paths = append(paths, f.paths.Stage, f.paths.Record)
						case "run-mode":
							if err := os.Chmod(filepath.Dir(f.paths.Stage), 0750); err != nil {
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
					result, err := f.execute(t.Context(), options, hooks)
					// Then: all X/original/stage/record assets survive exactly, D1 stays T.
					if !errors.Is(err, publication.ErrChanged) || !result.TMCommitted ||
						result.Publication.State != publication.NotPublished || reached != 1 {
						t.Fatalf("result=%+v error=%v reached=%d", result, err, reached)
					}
					actual := pipelineImages(t, paths)
					pipelineEqual(t, actual, checkpoint)
					pipelineJSON(t, "actual-files.json", actual)
					pipelineEqual(t, pipelineReadRows(t, f.db), f.expectedRows(t))
					pipelineJSON(t, "expected-rows.json", f.expectedRows(t))
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
