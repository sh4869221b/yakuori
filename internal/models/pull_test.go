package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
)

type cancelOnCloseEngine struct {
	inference.Engine
	cancel context.CancelFunc
}

func (e *cancelOnCloseEngine) Close() error {
	e.cancel()
	return nil
}

// pullFixture is a private registry, the bytes its TLS fixture serves, and a
// loader that accepts the stage only when it is bound to the expected identity.
type pullFixture struct {
	registry *Registry
	payload  []byte
	digest   string
	one      Entry
	loaded   []string
	runCtx   context.Context
	used     PullRequest
	final    string
	plain    atomic.Int64
	hops     *atomic.Int64
}

func newPullFixture(t *testing.T) *pullFixture {
	t.Helper()
	root := t.TempDir()
	r := New(config.Paths{Config: filepath.Join(root, "config"), Data: filepath.Join(root, "data"), Cache: filepath.Join(root, "cache"), State: filepath.Join(root, "state")})
	f := &pullFixture{registry: r, payload: []byte("GGUF fixture bytes for pull")}
	sum := sha256.Sum256(f.payload)
	f.digest = hex.EncodeToString(sum[:])
	r.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f.loaded = append(f.loaded, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(data)
		if options.ExpectedModelSHA256 == nil || digest != *options.ExpectedModelSHA256 {
			return nil, errors.New("stage bytes do not match the expected identity")
		}
		if options.Backend != "cpu" || options.ComputeQuant != "int4" {
			return nil, fmt.Errorf("unexpected loader options: %q %q", options.Backend, options.ComputeQuant)
		}
		return &testEngine{}, nil
	}
	return f
}

func (f *pullFixture) request(url string) PullRequest {
	return PullRequest{URL: url, ID: "fetched", SHA256: f.digest, SizeBytes: int64(len(f.payload)), DigestSource: "plan-pinned test fixture digest"}
}

func (f *pullFixture) context() context.Context {
	if f.runCtx != nil {
		return f.runCtx
	}
	return context.Background()
}

func (f *pullFixture) pullWith(t *testing.T, srv *httptest.Server, req PullRequest) error {
	t.Helper()
	f.used = req
	_, err := f.registry.pull(f.context(), pullClient(t, srv), req)
	return err
}

func (f *pullFixture) pull(t *testing.T, srv *httptest.Server) error {
	t.Helper()
	return f.pullWith(t, srv, f.request(srv.URL))
}

// registerOne puts a selected model in place, so every case can prove that a
// pull changes neither the selection nor the model already there.
func registerOne(t *testing.T, f *pullFixture) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "source.gguf")
	write(t, source, []byte("managed model"))
	entry, err := f.registry.Import(context.Background(), "one", source)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.registry.Use(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	f.one = entry
}

func tlsServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// pullClient keeps TLS verification real (the fixture CA) and reuses the
// production transport and redirect policy.
func pullClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	base, ok := srv.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("unexpected test transport")
	}
	transport := pullTransport()
	transport.TLSClientConfig = base.TLSClientConfig
	return newPullClient(transport)
}

// snapshot maps every path below root to its kind or contents, so a failed pull
// is compared against the exact state it must leave behind.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return entries
	}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			entries[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

type pullCase struct {
	name    string
	payload []byte
	prepare func(t *testing.T, f *pullFixture)
	run     func(t *testing.T, f *pullFixture) error
	success bool
	check   func(t *testing.T, f *pullFixture, err error)
}

func TestPull(t *testing.T) {
	for _, tc := range []pullCase{{
		name:    "success",
		success: true,
		run: func(t *testing.T, f *pullFixture) error {
			var hits atomic.Int64
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if encoding := r.Header.Get("Accept-Encoding"); encoding != "" {
					t.Errorf("pull asked for compression: %q", encoding)
				}
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/model", http.StatusFound)
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(f.payload)))
				w.Write(f.payload)
			}))
			t.Cleanup(srv.Close)
			f.final = srv.URL + "/model"
			err := f.pullWith(t, srv, f.request(srv.URL+"/start"))
			if hits.Load() != 2 {
				t.Errorf("fixture answered %d requests, want 2 (one redirect)", hits.Load())
			}
			return err
		},
	}, {
		name: "no replay of a redirected request",
		run: func(t *testing.T, f *pullFixture) error {
			var target atomic.Int64
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/model", http.StatusFound)
					return
				}
				// The redirect is followed over the keep-alive connection the
				// fixture answered on, and that connection dies before the
				// first byte of the response: the case net/http answers by
				// re-sending a replayable GET. The second request is served, so
				// a replay shows up as this pull succeeding, never as a flake.
				if target.Add(1) > 1 {
					w.Header().Set("Content-Length", strconv.Itoa(len(f.payload)))
					w.Write(f.payload)
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				defer conn.Close()
			}))
			t.Cleanup(srv.Close)
			err := f.pullWith(t, srv, f.request(srv.URL+"/start"))
			if got := target.Load(); got != 1 {
				t.Errorf("redirect target answered %d requests, want 1: the transport replayed the request", got)
			}
			return err
		},
	}, {
		name:    "single request protocol with an HTTP2 server",
		success: true,
		run: func(t *testing.T, f *pullFixture) error {
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.ProtoMajor != 1 {
					t.Errorf("pull negotiated HTTP/%d, want HTTP/1 without protocol-level retries", r.ProtoMajor)
				}
				w.Write(f.payload)
			}))
			srv.EnableHTTP2 = true
			srv.StartTLS()
			t.Cleanup(srv.Close)
			client := pullClient(t, srv)
			// Restore normal HTTP/2 eligibility after the fixture adds its TLS CA.
			client.Transport.(*http.Transport).ForceAttemptHTTP2 = true
			f.used = f.request(srv.URL)
			_, err := f.registry.pull(f.context(), client, f.used)
			return err
		},
	}, {
		name: "short body",
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, f.payload[:len(f.payload)-3]))
		},
	}, {
		name: "oversize body",
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, append(append([]byte{}, f.payload...), 'x')))
		},
	}, {
		name: "same size wrong digest",
		run: func(t *testing.T, f *pullFixture) error {
			body := append([]byte{}, f.payload...)
			body[0] ^= 0xff
			return f.pull(t, tlsServer(t, body))
		},
	}, {
		name: "invalid gguf accepted identity",
		prepare: func(t *testing.T, f *pullFixture) {
			f.payload = []byte("this is not a gguf model")
			sum := sha256.Sum256(f.payload)
			f.digest = hex.EncodeToString(sum[:])
			real := New(f.registry.paths).open
			f.registry.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
				f.loaded = append(f.loaded, path)
				return real(ctx, path, options)
			}
		},
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, f.payload))
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if len(f.loaded) != 1 {
				t.Fatalf("real loader reached %d times, want 1", len(f.loaded))
			}
		},
	}, {
		name: "non 200",
		run: func(t *testing.T, f *pullFixture) error {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "gone", http.StatusNotFound)
			}))
			t.Cleanup(srv.Close)
			return f.pull(t, srv)
		},
	}, {
		name: "https to http redirect",
		run: func(t *testing.T, f *pullFixture) error {
			plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				f.plain.Add(1)
				w.Write(f.payload)
			}))
			t.Cleanup(plain.Close)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, plain.URL+"/model", http.StatusFound)
			}))
			t.Cleanup(srv.Close)
			return f.pull(t, srv)
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if f.plain.Load() != 0 {
				t.Fatalf("followed a redirect to a plain HTTP server %d times", f.plain.Load())
			}
		},
	}, {
		name: "redirect limit",
		run: func(t *testing.T, f *pullFixture) error {
			var hops atomic.Int64
			f.hops = &hops
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hops.Add(1)
				http.Redirect(w, r, "/next", http.StatusFound)
			}))
			t.Cleanup(srv.Close)
			return f.pull(t, srv)
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if got := f.hops.Load(); got != maxPullRedirects {
				t.Fatalf("followed %d redirects, want %d", got, maxPullRedirects)
			}
		},
	}, {
		name: "truncated body",
		run: func(t *testing.T, f *pullFixture) error {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Errorf("hijack: %v", err)
					return
				}
				defer conn.Close()
				fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n", len(f.payload))
				conn.Write(f.payload[:4])
			}))
			t.Cleanup(srv.Close)
			return f.pull(t, srv)
		},
	}, {
		name: "body cancellation",
		run: func(t *testing.T, f *pullFixture) error {
			flushed := make(chan struct{})
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", strconv.Itoa(len(f.payload)))
				w.Write(f.payload[:4])
				w.(http.Flusher).Flush()
				close(flushed)
				<-r.Context().Done()
			}))
			t.Cleanup(srv.Close)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				<-flushed
				cancel()
			}()
			f.runCtx = ctx
			return f.pull(t, srv)
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled body: %v", err)
			}
		},
	}, {
		name: "cancellation during validation",
		prepare: func(t *testing.T, f *pullFixture) {
			ctx, cancel := context.WithCancel(context.Background())
			f.runCtx = ctx
			f.registry.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
				cancel()
				f.loaded = append(f.loaded, path)
				return nil, ctx.Err()
			}
		},
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, f.payload))
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled validation: %v", err)
			}
		},
	}, {
		name: "cancellation after validation",
		prepare: func(t *testing.T, f *pullFixture) {
			ctx, cancel := context.WithCancel(context.Background())
			f.runCtx = ctx
			f.registry.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
				f.loaded = append(f.loaded, path)
				return &cancelOnCloseEngine{cancel: cancel}, nil
			}
		},
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, f.payload))
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled before the commit: %v", err)
			}
		},
	}, {
		name: "lock busy",
		prepare: func(t *testing.T, f *pullFixture) {
			lock, err := f.registry.lock()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { lock.Close() })
		},
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, f.payload))
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if !errors.Is(err, ErrBusy) {
				t.Fatalf("busy lock: %v", err)
			}
		},
	}, {
		name: "collision",
		run: func(t *testing.T, f *pullFixture) error {
			srv := tlsServer(t, f.payload)
			req := f.request(srv.URL)
			req.ID = "one"
			return f.pullWith(t, srv, req)
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if !errors.Is(err, ErrCollision) {
				t.Fatalf("collision: %v", err)
			}
		},
	}, {
		name: "registry save failure",
		prepare: func(t *testing.T, f *pullFixture) {
			if os.Geteuid() == 0 {
				t.Skip("permission failure requires non-root")
			}
			open := f.registry.open
			f.registry.open = func(ctx context.Context, path string, options goinfer.Options) (inference.Engine, error) {
				engine, err := open(ctx, path, options)
				if err := os.Chmod(f.registry.paths.Data, 0o500); err != nil {
					t.Fatal(err)
				}
				return engine, err
			}
			t.Cleanup(func() { os.Chmod(f.registry.paths.Data, 0o700) })
		},
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, f.payload))
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPullFixture(t)
			if tc.payload != nil {
				f.payload = tc.payload
			}
			registerOne(t, f)
			if tc.prepare != nil {
				tc.prepare(t, f)
			}
			f.loaded = nil
			before := snapshot(t, filepath.Dir(f.registry.paths.Data))
			err := tc.run(t, f)
			if tc.success {
				if err != nil {
					t.Fatalf("pull: %v", err)
				}
				checkPulledModel(t, f)
				return
			}
			if err == nil {
				t.Fatal("pull unexpectedly succeeded")
			}
			if tc.check != nil {
				tc.check(t, f, err)
			}
			if after := snapshot(t, filepath.Dir(f.registry.paths.Data)); !maps.Equal(before, after) {
				t.Fatalf("failed pull changed storage:\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}

// TestPullInvalidRequest proves an unusable request is rejected before any
// storage exists and before a connection is opened.
func TestPullInvalidRequest(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("unexpected"))
	}))
	t.Cleanup(srv.Close)
	client := pullClient(t, srv)
	for _, tc := range []struct {
		name   string
		mutate func(*PullRequest)
	}{
		{"invalid ID", func(r *PullRequest) { r.ID = "../escape" }},
		{"mixed case digest", func(r *PullRequest) { r.SHA256 = strings.ToUpper(r.SHA256) }},
		{"truncated digest", func(r *PullRequest) { r.SHA256 = r.SHA256[:63] }},
		{"non hexadecimal digest", func(r *PullRequest) { r.SHA256 = strings.Repeat("z", 64) }},
		{"zero size", func(r *PullRequest) { r.SizeBytes = 0 }},
		{"negative size", func(r *PullRequest) { r.SizeBytes = -1 }},
		{"size that cannot grow by one", func(r *PullRequest) { r.SizeBytes = math.MaxInt64 }},
		{"blank digest source", func(r *PullRequest) { r.DigestSource = " \t" }},
		{"plain http URL", func(r *PullRequest) { r.URL = "http://" + strings.TrimPrefix(r.URL, "https://") }},
		{"URL with userinfo", func(r *PullRequest) { r.URL = "https://user@example.invalid/model" }},
		{"URL with fragment", func(r *PullRequest) { r.URL += "#model" }},
		{"relative URL", func(r *PullRequest) { r.URL = "/model" }},
		{"port-only authority URL", func(r *PullRequest) { r.URL = "https://:443/model.gguf" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPullFixture(t)
			req := f.request(srv.URL)
			tc.mutate(&req)
			if _, err := f.registry.pull(context.Background(), client, req); err == nil {
				t.Fatal("invalid request was accepted")
			}
			if hits.Load() != 0 {
				t.Fatalf("invalid request opened %d connections", hits.Load())
			}
			for _, path := range []string{f.registry.paths.Data, f.registry.paths.Cache} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid request created %s", path)
				}
			}
		})
	}
}

// TestPullValidAuthorities proves the tightened authority check keeps
// accepting a valid hostname and an IPv6 literal, each with an explicit port.
func TestPullValidAuthorities(t *testing.T) {
	for _, source := range []string{"https://example.invalid:8443/model.gguf", "https://[::1]:8443/model.gguf"} {
		req := PullRequest{URL: source, ID: "fetched", SHA256: strings.Repeat("a", 64), SizeBytes: 1024, DigestSource: "plan-pinned test fixture digest"}
		if err := validatePullRequest(req); err != nil {
			t.Fatalf("valid authority %s rejected: %v", source, err)
		}
	}
}

func checkPulledModel(t *testing.T, f *pullFixture) {
	t.Helper()
	r := f.registry
	entries, err := r.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Entry{SHA256: f.digest, SizeBytes: int64(len(f.payload))}
	if len(entries) != 2 || entries["fetched"] != want || entries["one"] != f.one {
		t.Fatalf("registry after pull = %v, want one=%v fetched=%v", entries, f.one, want)
	}
	if id, err := r.Selected(); err != nil || id != "one" {
		t.Fatalf("pull changed the selection: %q %v", id, err)
	}
	if got := read(t, r.modelPath("fetched")); string(got) != string(f.payload) {
		t.Fatalf("registered bytes = %q, want the fixture payload", got)
	}
	if got := read(t, r.modelPath("one")); string(got) != "managed model" {
		t.Fatalf("existing model changed: %q", got)
	}
	if len(f.loaded) != 1 || filepath.Base(f.loaded[0]) != "model.gguf" || !strings.HasPrefix(filepath.Base(filepath.Dir(f.loaded[0])), ".pull-") {
		t.Fatalf("loader did not validate the acquisition stage: %v", f.loaded)
	}
	sidecar := filepath.Join(filepath.Dir(r.modelPath("fetched")), "provenance.json")
	decoder := json.NewDecoder(strings.NewReader(string(read(t, sidecar))))
	decoder.DisallowUnknownFields()
	var got provenance
	if err := decoder.Decode(&got); err != nil {
		t.Fatal(err)
	}
	final := f.final
	if final == "" {
		final = f.used.URL
	}
	if got.Schema != 1 || got.SourceURL != f.used.URL || got.FinalURL != final || got.ExpectedSHA256 != f.digest || got.ExpectedSizeBytes != int64(len(f.payload)) || got.DigestSource != f.used.DigestSource {
		t.Fatalf("provenance = %+v for request %+v", got, f.used)
	}
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Dir(r.modelPath("fetched")), 0o700},
		{r.modelPath("fetched"), 0o600},
		{sidecar, 0o600},
	} {
		info, err := os.Stat(dir.path)
		if err != nil || info.Mode().Perm() != dir.mode {
			t.Fatalf("%s: mode %v err %v", dir.path, info, err)
		}
	}
}

var leftoverPaths = []string{
	"data/models/.pull-abandoned",
	"data/models/.pull-abandoned/model.gguf",
	"data/models/.pull-abandoned/partial",
	"data/models/.import-abandoned",
	"data/models/.import-abandoned/model.gguf",
	"data/models/leftover",
	"data/models/leftover/owner-file",
}

// placeLeftovers puts an earlier run's stage, an unregistered destination and
// an import stage on disk; none of them belongs to the next pull.
func placeLeftovers(t *testing.T, f *pullFixture) {
	t.Helper()
	models := f.registry.storage.ModelsDir
	placeFile(t, filepath.Join(models, ".pull-abandoned", "model.gguf"), []byte("abandoned pull stage"))
	placeFile(t, filepath.Join(models, ".pull-abandoned", "partial"), []byte("partial"))
	placeFile(t, filepath.Join(models, ".import-abandoned", "model.gguf"), []byte("abandoned import stage"))
	placeFile(t, filepath.Join(models, "leftover", "owner-file"), []byte("external owner"))
}

func placeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, path, data)
}

func TestPullLeftovers(t *testing.T) {
	for _, tc := range []pullCase{{
		name: "failed pull removes only its own stage",
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, f.payload[:len(f.payload)-3]))
		},
	}, {
		name: "collision with an unregistered destination keeps it",
		run: func(t *testing.T, f *pullFixture) error {
			srv := tlsServer(t, f.payload)
			req := f.request(srv.URL)
			req.ID = "leftover"
			return f.pullWith(t, srv, req)
		},
		check: func(t *testing.T, f *pullFixture, err error) {
			if !errors.Is(err, ErrCollision) {
				t.Fatalf("collision: %v", err)
			}
		},
	}, {
		name:    "successful pull keeps foreign leftovers",
		success: true,
		run: func(t *testing.T, f *pullFixture) error {
			return f.pull(t, tlsServer(t, f.payload))
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPullFixture(t)
			registerOne(t, f)
			placeLeftovers(t, f)
			f.loaded = nil
			before := snapshot(t, filepath.Dir(f.registry.paths.Data))
			err := tc.run(t, f)
			if tc.success && err != nil {
				t.Fatalf("pull: %v", err)
			}
			if !tc.success && err == nil {
				t.Fatal("pull unexpectedly succeeded")
			}
			if tc.check != nil {
				tc.check(t, f, err)
			}
			after := snapshot(t, filepath.Dir(f.registry.paths.Data))
			for _, path := range leftoverPaths {
				if before[path] != after[path] {
					t.Fatalf("leftover %s changed: %q -> %q", path, before[path], after[path])
				}
			}
			if tc.success {
				checkPulledModel(t, f)
				delete(after, "data/registry.json")
				delete(before, "data/registry.json")
				delete(after, "data/models/fetched")
				delete(after, "data/models/fetched/model.gguf")
				delete(after, "data/models/fetched/provenance.json")
			}
			if !maps.Equal(before, after) {
				t.Fatalf("storage not as expected:\nbefore=%v\nafter=%v", before, after)
			}
		})
	}
}
