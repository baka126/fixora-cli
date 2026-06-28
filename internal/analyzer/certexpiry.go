package analyzer

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"time"
)

// certExpiryWarningWindow is how far ahead of NotAfter a certificate is flagged
// as expiring soon.
const certExpiryWarningWindow = 30 * 24 * time.Hour

// tlsCrtBytes base64-decodes data["tls.crt"] from a Secret object. It reads ONLY
// the public certificate field — never tls.key or any other Secret data. ok is
// false when tls.crt is absent or not decodable.
func tlsCrtBytes(secret map[string]any) ([]byte, bool) {
	data, ok := secret["data"].(map[string]any)
	if !ok {
		return nil, false
	}
	raw, ok := data["tls.crt"].(string)
	if !ok || raw == "" {
		return nil, false
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, false
	}
	return decoded, true
}

// leafCertNotAfter parses the first PEM CERTIFICATE block (the server/leaf cert)
// and returns its NotAfter and Subject CN. Intermediates are ignored.
func leafCertNotAfter(crt []byte) (time.Time, string, error) {
	block, _ := pem.Decode(crt)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, "", fmt.Errorf("no PEM CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, "", err
	}
	return cert.NotAfter, cert.Subject.CommonName, nil
}

// classifyCertExpiry decides the finding for a certificate NotAfter relative to
// now. flag is false when the certificate is valid and not within the warning
// window (no finding).
func classifyCertExpiry(notAfter, now time.Time) (status, severity string, flag bool) {
	switch {
	case notAfter.Before(now):
		return "TLSCertExpired", "high", true
	case notAfter.Before(now.Add(certExpiryWarningWindow)):
		return "TLSCertExpiringSoon", "medium", true
	default:
		return "", "", false
	}
}
