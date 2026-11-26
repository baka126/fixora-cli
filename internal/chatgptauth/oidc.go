package chatgptauth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"
)

type identityClaims struct {
	Issuer          string          `json:"iss"`
	Subject         string          `json:"sub"`
	Audience        json.RawMessage `json:"aud"`
	AuthorizedParty string          `json:"azp"`
	Expiry          int64           `json:"exp"`
	NotBefore       int64           `json:"nbf"`
	Nonce           string          `json:"nonce"`
	Email           string          `json:"email"`
}

type jwtHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

type jwkSet struct {
	Keys []jwk `json:"keys"`
}

type jwk struct {
	KeyType string `json:"kty"`
	Use     string `json:"use"`
	KeyID   string `json:"kid"`
	N       string `json:"n"`
	E       string `json:"e"`
	X       string `json:"x"`
	Y       string `json:"y"`
}

func validateIDToken(ctx context.Context, doc discovery, token, clientID, nonce string) (identityClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return identityClaims{}, errors.New("invalid ID token")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return identityClaims{}, errors.New("invalid ID token header")
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.KeyID == "" {
		return identityClaims{}, errors.New("invalid ID token header")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return identityClaims{}, errors.New("invalid ID token claims")
	}
	var claims identityClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return identityClaims{}, errors.New("invalid ID token claims")
	}
	if claims.Issuer != doc.Issuer || claims.Subject == "" || claims.Expiry <= time.Now().Unix() || (claims.NotBefore != 0 && claims.NotBefore > time.Now().Unix()) || claims.Nonce != nonce || !audienceContains(claims.Audience, clientID) || (audienceCount(claims.Audience) > 1 && claims.AuthorizedParty != clientID) {
		return identityClaims{}, errors.New("ID token claims did not match this sign-in")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.JWKSURI, nil)
	if err != nil {
		return identityClaims{}, errors.New("could not validate ID token")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return identityClaims{}, errors.New("could not validate ID token signature")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return identityClaims{}, errors.New("could not validate ID token signature")
	}
	var keys jwkSet
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&keys); err != nil {
		return identityClaims{}, errors.New("could not read ID token signing keys")
	}
	var signingKey *jwk
	for i := range keys.Keys {
		if keys.Keys[i].KeyID == header.KeyID && (keys.Keys[i].Use == "" || keys.Keys[i].Use == "sig") {
			signingKey = &keys.Keys[i]
			break
		}
	}
	if signingKey == nil || verifyJWTSignature(header.Algorithm, *signingKey, parts[0]+"."+parts[1], parts[2]) != nil {
		return identityClaims{}, errors.New("ID token signature was invalid")
	}
	return claims, nil
}

func verifyJWTSignature(algorithm string, key jwk, message, signature string) error {
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(message))
	switch algorithm {
	case "RS256":
		if key.KeyType != "RSA" {
			return errors.New("key algorithm mismatch")
		}
		n, err := decodeBigInt(key.N)
		if err != nil {
			return err
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
		if err != nil || len(eBytes) == 0 || len(eBytes) > 4 {
			return errors.New("invalid RSA exponent")
		}
		e := 0
		for _, b := range eBytes {
			e = e<<8 | int(b)
		}
		return rsa.VerifyPKCS1v15(&rsa.PublicKey{N: n, E: e}, crypto.SHA256, digest[:], sig)
	case "ES256":
		if key.KeyType != "EC" {
			return errors.New("key algorithm mismatch")
		}
		x, err := decodeBigInt(key.X)
		if err != nil {
			return err
		}
		y, err := decodeBigInt(key.Y)
		if err != nil {
			return err
		}
		if !elliptic.P256().IsOnCurve(x, y) || len(sig) != 64 {
			return errors.New("invalid EC signature")
		}
		return verifyECDSA(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], sig)
	default:
		return errors.New("unsupported ID token signing algorithm")
	}
}

func verifyECDSA(key *ecdsa.PublicKey, digest, signature []byte) error {
	if !ecdsa.Verify(key, digest, new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return errors.New("invalid EC signature")
	}
	return nil
}

func decodeBigInt(raw string) (*big.Int, error) {
	value, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(value) == 0 {
		return nil, errors.New("invalid signing key")
	}
	return new(big.Int).SetBytes(value), nil
}

func audienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) == nil {
		for _, value := range multiple {
			if value == expected {
				return true
			}
		}
	}
	return false
}

func audienceCount(raw json.RawMessage) int {
	var single string
	if json.Unmarshal(raw, &single) == nil && single != "" {
		return 1
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) == nil {
		return len(multiple)
	}
	return 0
}
