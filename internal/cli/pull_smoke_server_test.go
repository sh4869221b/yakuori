//go:build modelsmoke

package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// pullSmokeGate replaces polling: reached fires when the fixture flushed the
// first chunk, release lets the handler stop.
type pullSmokeGate struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newPullSmokeGate(t *testing.T) *pullSmokeGate {
	t.Helper()
	gate := &pullSmokeGate{reached: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(gate.open)
	return gate
}

func (g *pullSmokeGate) open() { g.once.Do(func() { close(g.release) }) }

// pullSmokePayload with a positive first sends that many bytes, flushes,
// signals reached, then stalls so a cancellation can land mid-transfer.
type pullSmokePayload struct {
	body    []byte
	status  int
	first   int
	reached chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (h *pullSmokePayload) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.status != 0 {
		w.WriteHeader(h.status)
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(h.body)))
	w.WriteHeader(http.StatusOK)
	if h.first > 0 {
		_, _ = w.Write(h.body[:h.first])
		w.(http.Flusher).Flush()
		if h.reached != nil {
			h.once.Do(func() { close(h.reached) })
		}
		select {
		case <-h.release:
		case <-r.Context().Done():
		}
		return
	}
	_, _ = w.Write(h.body)
}

// pullSmokeFile streams a fixture file instead of buffering it in memory.
type pullSmokeFile struct{ path string }

func (h pullSmokeFile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	file, err := os.Open(h.path)
	if err != nil {
		http.Error(w, "fixture unavailable", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		http.Error(w, "fixture unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, file)
}

type pullSmokeServer struct {
	url    string
	caFile string
}

// newPullSmokeServer generates the CA itself and reveals it to the child only
// through SSL_CERT_FILE, so no test ever disables TLS verification.
func newPullSmokeServer(t *testing.T, handler http.Handler) *pullSmokeServer {
	t.Helper()
	caPEM, certificate := newPullSmokeCertificate(t)
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return &pullSmokeServer{url: server.URL, caFile: caFile}
}

func newPullSmokeCertificate(t *testing.T) ([]byte, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "yakuori modelsmoke root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "yakuori modelsmoke fixture"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		tls.Certificate{Certificate: [][]byte{leafDER}, PrivateKey: leafKey}
}
