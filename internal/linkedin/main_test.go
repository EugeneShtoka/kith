package linkedin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// No test reaches linkedin.com. linkedingo sends its requests to LinkedIn's own
// addresses through the proxy the environment names, so TestMain names one that hands
// them to a fake LinkedIn, whose certificate only this test process trusts
// (SSL_CERT_FILE). A test answers the requests carrying its session's li_at cookie
// (fakeLinkedIn); any other is refused.
func TestMain(m *testing.M) {
	os.Exit(withFakeLinkedIn(m))
}

// fakes are the tests' handlers, by the li_at cookie their sessions carry.
var fakes sync.Map

// fakeLinkedIn answers the requests from sessions whose li_at cookie is liAt with h,
// for the rest of t.
func fakeLinkedIn(t *testing.T, liAt string, h http.Handler) {
	t.Helper()
	fakes.Store(liAt, h)
	t.Cleanup(func() { fakes.Delete(liAt) })
}

func withFakeLinkedIn(m *testing.M) int {
	dir, err := os.MkdirTemp("", "linkedin-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cert, err := linkedInCertificate(filepath.Join(dir, "linkedin.pem"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	site := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if session, err := r.Cookie(cookieSession); err == nil {
			if h, ok := fakes.Load(session.Value); ok {
				h.(http.Handler).ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, "no test answers this session", http.StatusForbidden)
	}))
	site.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	site.StartTLS()
	defer site.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tunnel(w, r, site.Listener.Addr().String())
	}))
	defer proxy.Close()
	for key, value := range map[string]string{
		"HTTPS_PROXY": proxy.URL, "https_proxy": "", "NO_PROXY": "", "no_proxy": "",
		"SSL_CERT_FILE": filepath.Join(dir, "linkedin.pem"), "SSL_CERT_DIR": dir,
	} {
		if err := os.Setenv(key, value); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return m.Run()
}

// tunnel is a proxy's CONNECT, to the fake LinkedIn at site whatever the address asked.
func tunnel(w http.ResponseWriter, r *http.Request, site string) {
	if r.Method != http.MethodConnect {
		http.Error(w, "only CONNECT", http.StatusMethodNotAllowed)
		return
	}
	upstream, err := (&net.Dialer{}).DialContext(r.Context(), "tcp", site)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hijacker, _ := w.(http.Hijacker)
	if hijacker == nil {
		_ = upstream.Close()
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
	go func() {
		_, _ = io.Copy(upstream, buffered)
		_ = upstream.Close()
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
	}()
}

// linkedInCertificate is a self-signed certificate for LinkedIn's address, written to
// path as the one root to trust.
func linkedInCertificate(path string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake LinkedIn"},
		DNSNames: []string{"www.linkedin.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
