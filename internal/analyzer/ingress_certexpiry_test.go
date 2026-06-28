package analyzer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tlsSecretWithCert(t *testing.T, notAfter time.Time) map[string]any {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "example.com"},
		NotBefore:    notAfter.Add(-365 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, _ := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	crtPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "web-tls"},
		"data":     map[string]any{"tls.crt": base64.StdEncoding.EncodeToString(crtPEM)},
	}
}

func ingressWithTLS() []map[string]any {
	return []map[string]any{{
		"metadata": map[string]any{"namespace": "prod", "name": "web"},
		"spec": map[string]any{
			"ingressClassName": "nginx",
			"tls":              []any{map[string]any{"secretName": "web-tls", "hosts": []any{"example.com"}}},
		},
	}}
}

func certExpiryCtx(t *testing.T, secret map[string]any, calls *int32) (*ScanContext, Analyzer, Options) {
	t.Helper()
	reader := fakeReader{
		items:            map[string][]map[string]any{"ingresses": ingressWithTLS()},
		resource:         secret,
		getResourceCalls: calls,
	}
	opts := Options{Namespace: "prod", CheckCertExpiry: true}
	return NewScanContext(context.Background(), reader, opts), New(reader, opts), opts
}

func TestIngressExpiredCertFlagged(t *testing.T) {
	secret := tlsSecretWithCert(t, time.Now().Add(-time.Hour))
	ctx, a, _ := certExpiryCtx(t, secret, nil)
	findings, err := a.analyzeIngressBackends(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if hasStatus(findings, "TLSCertExpired") == nil {
		t.Fatalf("expected TLSCertExpired, got %+v", findings)
	}
}

func TestIngressValidCertNotFlagged(t *testing.T) {
	secret := tlsSecretWithCert(t, time.Now().Add(400*24*time.Hour))
	ctx, a, _ := certExpiryCtx(t, secret, nil)
	findings, _ := a.analyzeIngressBackends(ctx)
	if hasStatus(findings, "TLSCertExpired") != nil || hasStatus(findings, "TLSCertExpiringSoon") != nil {
		t.Fatalf("valid cert must not be flagged: %+v", findings)
	}
}

func TestIngressCertExpiryOptInOffReadsNoSecret(t *testing.T) {
	var calls int32
	secret := tlsSecretWithCert(t, time.Now().Add(-time.Hour))
	reader := fakeReader{
		items:            map[string][]map[string]any{"ingresses": ingressWithTLS()},
		resource:         secret,
		getResourceCalls: &calls,
	}
	opts := Options{Namespace: "prod", CheckCertExpiry: false}
	ctx := NewScanContext(context.Background(), reader, opts)
	findings, _ := New(reader, opts).analyzeIngressBackends(ctx)
	if hasStatus(findings, "TLSCertExpired") != nil {
		t.Fatal("opt-in off must not flag cert expiry")
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("opt-in off must not read Secret data; GetResource called %d times", calls)
	}
}

func TestIngressUnparseableCert(t *testing.T) {
	secret := map[string]any{
		"metadata": map[string]any{"namespace": "prod", "name": "web-tls"},
		"data":     map[string]any{"tls.crt": base64.StdEncoding.EncodeToString([]byte("garbage"))},
	}
	ctx, a, _ := certExpiryCtx(t, secret, nil)
	findings, _ := a.analyzeIngressBackends(ctx)
	if hasStatus(findings, "TLSCertUnreadable") == nil {
		t.Fatalf("unparseable cert must yield TLSCertUnreadable: %+v", findings)
	}
}

func TestCertExpiryStatusesDoNotCollideWithPlannerKeys(t *testing.T) {
	statuses := []string{"TLSCertExpired", "TLSCertExpiringSoon", "TLSCertUnreadable"}
	plannerKeys := []string{
		"ImagePull", "OOMKilled", "CrashLoopBackOff", "CreateContainerConfigError",
		"ExecFormatError", "PermissionDenied", "NoEndpoints", "ConnectionRefused",
		"HPA", "Evicted", "Webhook",
	}
	for _, s := range statuses {
		for _, key := range plannerKeys {
			if strings.Contains(s, key) || strings.Contains(key, s) {
				t.Fatalf("status %q collides with planner key %q", s, key)
			}
		}
	}
}
