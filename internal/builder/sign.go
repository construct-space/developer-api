package builder

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// signSpaceBundle signs the bundle's app.iife.js with a P-256 private key and
// writes build.signature + build.signKeyId into the bundled manifest.json.
//
// The signature is raw IEEE-P1363 (r||s, each 32 bytes big-endian, 64 total),
// base64-encoded - the format the host verifier (crypto.subtle ECDSA) expects.
// It is NOT ASN.1 DER (Go's ecdsa.SignASN1 default), which would fail to verify.
//
// app.iife.js is left untouched, so manifest.build.checksum (a SHA-256 over the
// JS) stays valid; only manifest.json gains the signature fields.
//
// The private key is read from SPACE_SIGNING_KEY_PEM (PEM content) or, if that
// is empty, the file at SPACE_SIGNING_KEY_FILE. When neither is set this is a
// no-op (nil) so unsigned publishing keeps working unchanged. Key id comes from
// SPACE_SIGNING_KEY_ID (default "marketplace-2026-05").
func signSpaceBundle(spaceDir string) error {
	keyPEM := os.Getenv("SPACE_SIGNING_KEY_PEM")
	if keyPEM == "" {
		if f := os.Getenv("SPACE_SIGNING_KEY_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return fmt.Errorf("read SPACE_SIGNING_KEY_FILE: %w", err)
			}
			keyPEM = string(b)
		}
	}
	if keyPEM == "" {
		return nil // signing disabled
	}
	// CapRover (and most env-var UIs) store single-line values, so a pasted PEM
	// arrives with literal "\n" instead of real newlines. Normalize so the key
	// can be supplied as one line; pem.Decode needs real newlines.
	if !strings.Contains(keyPEM, "\n") && strings.Contains(keyPEM, "\\n") {
		keyPEM = strings.ReplaceAll(keyPEM, "\\n", "\n")
	}
	keyID := os.Getenv("SPACE_SIGNING_KEY_ID")
	if keyID == "" {
		keyID = "marketplace-2026-05"
	}

	priv, err := parseP256PrivateKey(keyPEM)
	if err != nil {
		return fmt.Errorf("parse signing key: %w", err)
	}

	jsBytes, err := os.ReadFile(filepath.Join(spaceDir, "app.iife.js"))
	if err != nil {
		return fmt.Errorf("read app.iife.js: %w", err)
	}

	sig, err := signP1363(priv, jsBytes)
	if err != nil {
		return fmt.Errorf("sign bundle: %w", err)
	}

	manPath := filepath.Join(spaceDir, "manifest.json")
	raw, err := os.ReadFile(manPath)
	if err != nil {
		return fmt.Errorf("read manifest.json: %w", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parse manifest.json: %w", err)
	}

	build, _ := manifest["build"].(map[string]any)
	if build == nil {
		build = map[string]any{}
	}
	build["signature"] = base64.StdEncoding.EncodeToString(sig)
	build["signKeyId"] = keyID
	manifest["build"] = build

	out, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest.json: %w", err)
	}
	if err := os.WriteFile(manPath, out, 0o644); err != nil {
		return fmt.Errorf("write manifest.json: %w", err)
	}
	return nil
}

// parseP256PrivateKey accepts PKCS8 (BEGIN PRIVATE KEY) or SEC1
// (BEGIN EC PRIVATE KEY) PEM and returns the ECDSA key.
func parseP256PrivateKey(pemStr string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, fmt.Errorf("invalid PEM")
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		ec, ok := k.(*ecdsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("PKCS8 key is not ECDSA")
		}
		return ec, nil
	}
	if ec, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return ec, nil
	}
	return nil, fmt.Errorf("unsupported private key format (want PKCS8 or SEC1 EC)")
}

// signP1363 returns an ECDSA P-256 / SHA-256 signature as raw r||s (64 bytes).
func signP1363(priv *ecdsa.PrivateKey, data []byte) ([]byte, error) {
	digest := sha256.Sum256(data)
	r, s, err := ecdsa.Sign(rand.Reader, priv, digest[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, 64)
	r.FillBytes(out[0:32])
	s.FillBytes(out[32:64])
	return out, nil
}
