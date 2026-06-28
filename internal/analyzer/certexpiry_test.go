package analyzer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// genCertPEM builds a self-signed leaf certificate PEM with the given NotAfter
// and CN. Test-only.
func genCertPEM(t *testing.T, cn string, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    notAfter.Add(-365 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestLeafCertNotAfter(t *testing.T) {
	want := time.Now().Add(100 * 24 * time.Hour).Truncate(time.Second)
	crt := genCertPEM(t, "example.com", want)
	got, cn, err := leafCertNotAfter(crt)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("notAfter = %v, want %v", got, want)
	}
	if cn != "example.com" {
		t.Fatalf("cn = %q, want example.com", cn)
	}
}

func TestLeafCertNotAfterGarbage(t *testing.T) {
	if _, _, err := leafCertNotAfter([]byte("not a pem cert")); err == nil {
		t.Fatal("expected error for non-PEM input")
	}
	if _, _, err := leafCertNotAfter(nil); err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestTlsCrtBytes(t *testing.T) {
	raw := base64.StdEncoding.EncodeToString([]byte("CERTDATA"))
	secret := map[string]any{"data": map[string]any{"tls.crt": raw}}
	crt, ok := tlsCrtBytes(secret)
	if !ok || string(crt) != "CERTDATA" {
		t.Fatalf("tlsCrtBytes = %q, %v", crt, ok)
	}
	// Only tls.key present -> must NOT read it.
	keyOnly := map[string]any{"data": map[string]any{"tls.key": raw}}
	if _, ok := tlsCrtBytes(keyOnly); ok {
		t.Fatal("tlsCrtBytes must not read tls.key")
	}
	if _, ok := tlsCrtBytes(map[string]any{}); ok {
		t.Fatal("absent data -> ok=false")
	}
}

func TestClassifyCertExpiry(t *testing.T) {
	now := time.Now()
	if s, sev, flag := classifyCertExpiry(now.Add(-time.Hour), now); !flag || s != "TLSCertExpired" || sev != "high" {
		t.Fatalf("expired: %q %q %v", s, sev, flag)
	}
	if s, sev, flag := classifyCertExpiry(now.Add(10*24*time.Hour), now); !flag || s != "TLSCertExpiringSoon" || sev != "medium" {
		t.Fatalf("soon: %q %q %v", s, sev, flag)
	}
	if _, _, flag := classifyCertExpiry(now.Add(400*24*time.Hour), now); flag {
		t.Fatal("far-future cert must not be flagged")
	}
}
