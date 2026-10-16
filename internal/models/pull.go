package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
)

// pullTimeout bounds one acquisition, including the transfer and the CPU
// validation that precedes the registry commit.
const pullTimeout = 30 * time.Minute

// maxPullRedirects is how many redirects a pull follows before it fails.
const maxPullRedirects = 10

// PullRequest is the minimal explicit acquisition input: where to fetch, the ID
// to register, and the identity the caller expects. DigestSource is the
// caller's own statement of where that digest came from; it is recorded as
// provenance and never checked or presented as a distribution guarantee.
type PullRequest struct {
	URL          string
	ID           string
	SHA256       string
	SizeBytes    int64
	DigestSource string
}

// provenance is stored beside the acquired bytes, so the origin of a managed
// model stays traceable without changing the registry schema.
type provenance struct {
	Schema            int    `json:"schema"`
	SourceURL         string `json:"source_url"`
	FinalURL          string `json:"final_url"`
	ExpectedSHA256    string `json:"expected_sha256"`
	ExpectedSizeBytes int64  `json:"expected_size_bytes"`
	DigestSource      string `json:"digest_source"`
}

// Pull explicitly fetches req.URL once and registers the received bytes under
// req.ID, but only when the body matches the expected size and SHA-256 and the
// local CPU loader accepts that same stage. Any failure before the registry
// commit leaves the registry untouched and removes this call's stage. A
// returned entry is valid only after the commit, so a non-zero entry means the
// model is registered even when err is non-nil; a post-commit failure never
// rolls the registration back. Pull never changes the selected model, never
// retries or resumes, and never touches stage leftovers of another run.
func (r *Registry) Pull(ctx context.Context, req PullRequest) (Entry, error) {
	transport := pullTransport()
	defer transport.CloseIdleConnections()
	return r.pull(ctx, newPullClient(transport), req)
}

// pull is the seam that carries an explicit client, so a test can supply its
// own transport without replacing a global one.
func (r *Registry) pull(ctx context.Context, client *http.Client, req PullRequest) (entry Entry, err error) {
	if err := validatePullRequest(req); err != nil {
		return Entry{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, pullTimeout)
	defer cancel()
	for _, path := range []string{r.paths.Data, r.storage.ModelsDir, r.paths.Cache, r.storage.SnapshotDir} {
		if err := privateDir(path, true); err != nil {
			return Entry{}, err
		}
	}
	stage, err := os.MkdirTemp(r.storage.ModelsDir, ".pull-*")
	if err != nil {
		return Entry{}, err
	}
	owned := stage
	defer func() {
		if owned != "" {
			err = errors.Join(err, os.RemoveAll(owned))
		}
	}()
	expected := Entry{SHA256: req.SHA256, SizeBytes: req.SizeBytes}
	model := filepath.Join(stage, "model.gguf")
	final, err := fetchModel(ctx, client, req, model)
	if err != nil {
		return Entry{}, err
	}
	// The loader receives the caller's expectation, so the stage bytes are
	// accepted by the same snapshot and digest contract as an imported model.
	engine, err := r.open(ctx, model, r.options(expected, "cpu"))
	if err != nil {
		return Entry{}, fmt.Errorf("validate pulled model: %w", err)
	}
	if err := engine.Close(); err != nil {
		return Entry{}, err
	}
	if err := writeProvenance(filepath.Join(stage, "provenance.json"), req, final); err != nil {
		return Entry{}, err
	}
	// publishStage owns the stage from here: it commits the model directory and
	// the registry document, or removes the stage again.
	owned = ""
	published, err := r.publishStage(ctx, req.ID, stage, expected)
	if published == (Entry{}) {
		return Entry{}, err
	}
	return published, err
}

// validatePullRequest rejects an unusable request before any storage is created
// or a connection is attempted.
func validatePullRequest(req PullRequest) error {
	if err := config.ValidateModelID(req.ID); err != nil {
		return err
	}
	if len(req.SHA256) != 64 || strings.ToLower(req.SHA256) != req.SHA256 {
		return errors.New("expected SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(req.SHA256); err != nil {
		return errors.New("expected SHA-256 must be 64 lowercase hexadecimal characters")
	}
	if req.SizeBytes <= 0 || req.SizeBytes == math.MaxInt64 {
		return errors.New("expected size must be a positive byte count that can grow by one")
	}
	if strings.TrimSpace(req.DigestSource) == "" {
		return errors.New("digest source must not be blank")
	}
	parsed, err := url.Parse(req.URL)
	if err != nil {
		return fmt.Errorf("source URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("source URL must be an absolute HTTPS URL without userinfo or fragment")
	}
	return nil
}

// pullTransport is the transport of one pull. Compression is disabled so the
// hashed bytes are the bytes the server sent.
func pullTransport() *http.Transport {
	return &http.Transport{DisableCompression: true}
}

// newPullClient builds the client for one pull: normal TLS verification, a
// redirect only to another HTTPS URL, and at most maxPullRedirects requests.
func newPullClient(transport http.RoundTripper) *http.Client {
	return &http.Client{Transport: transport, CheckRedirect: checkPullRedirect}
}

func checkPullRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return errors.New("refused a redirect to a non-HTTPS URL")
	}
	if len(via) >= maxPullRedirects {
		return fmt.Errorf("stopped after %d redirects", len(via))
	}
	return nil
}

// fetchModel streams the response body into path and returns the URL that
// answered with 200. The body is read up to the expected size plus one byte, so
// a longer body, a cut-off body and a digest mismatch are all judged from the
// received bytes; Content-Length is never the acceptance basis.
func fetchModel(ctx context.Context, client *http.Client, req PullRequest, path string) (final string, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return "", err
	}
	// On error the returned response, if any, already has a closed body.
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("pull %s: unexpected status %s", req.ID, response.Status)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, req.SizeBytes+1))
	if err := errors.Join(copyErr, file.Close()); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(err, ctxErr)
		}
		return "", err
	}
	if size > req.SizeBytes {
		return "", fmt.Errorf("pull %s: body exceeds the expected %d bytes", req.ID, req.SizeBytes)
	}
	if size != req.SizeBytes {
		return "", fmt.Errorf("pull %s: received %d bytes, expected %d", req.ID, size, req.SizeBytes)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != req.SHA256 {
		return "", fmt.Errorf("pull %s: expected SHA-256 %s, got %s", req.ID, req.SHA256, actual)
	}
	return response.Request.URL.String(), nil
}

// writeProvenance records the acquisition. It returns only once the sidecar is
// closed, so registration never covers a file that is still open.
func writeProvenance(path string, req PullRequest, finalURL string) (err error) {
	data, err := json.Marshal(provenance{
		Schema:            1,
		SourceURL:         req.URL,
		FinalURL:          finalURL,
		ExpectedSHA256:    req.SHA256,
		ExpectedSizeBytes: req.SizeBytes,
		DigestSource:      req.DigestSource,
	})
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}
