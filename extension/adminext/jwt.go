// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// jwtValidator validates HS256 (HMAC-SHA256) JWTs using only the standard
// library — no third-party JWT dependency. It enforces:
//
//   - Exactly three dot-separated segments.
//   - Header alg == "HS256" (rejects "none", RS256, etc. — prevents algorithm
//     confusion attacks).
//   - HMAC-SHA256 signature over the "header.payload" signing input, compared
//     in constant time.
//   - exp / nbf time claims when present.
//   - iss / aud claims when configured (non-empty) on JWTAuthConfig.
//
// Only HS256 is supported because JWTAuthConfig carries a single symmetric
// Secret. Asymmetric algorithms (RS256/ES256) are intentionally rejected.
type jwtValidator struct {
	secret   []byte
	issuer   string
	audience string
	now      func() time.Time
}

func newJWTValidator(cfg JWTAuthConfig) *jwtValidator {
	return &jwtValidator{
		secret:   []byte(cfg.Secret),
		issuer:   cfg.Issuer,
		audience: cfg.Audience,
		now:      time.Now,
	}
}

// validate returns nil if the token is a valid HS256 JWT matching the configured
// secret and (when set) issuer/audience, and not expired/not-yet-valid.
func (v *jwtValidator) validate(tokenStr string) error {
	parts := strings.Split(tokenStr, ".")
	if len(parts) != 3 {
		return errors.New("jwt: must have 3 segments")
	}
	signingInput := parts[0] + "." + parts[1]

	// Decode and verify header — alg confusion prevention.
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return fmt.Errorf("jwt: invalid header encoding: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return fmt.Errorf("jwt: invalid header json: %w", err)
	}
	if header.Alg != "HS256" {
		// Reject "none", RS256, ES256, etc. The secret is symmetric (HS256).
		return fmt.Errorf("jwt: unsupported alg %q (only HS256 is supported)", header.Alg)
	}

	// Verify signature (constant-time).
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("jwt: invalid signature encoding: %w", err)
	}
	mac := hmac.New(sha256.New, v.secret)
	mac.Write([]byte(signingInput))
	expected := mac.Sum(nil)
	if !hmac.Equal(expected, sig) {
		return errors.New("jwt: signature mismatch")
	}

	// Decode and validate claims.
	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("jwt: invalid payload encoding: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return fmt.Errorf("jwt: invalid payload json: %w", err)
	}
	now := v.now()
	if err := validateTimeClaims(claims, now); err != nil {
		return err
	}
	if v.issuer != "" {
		if iss, _ := claims["iss"].(string); iss != v.issuer {
			return fmt.Errorf("jwt: issuer %q does not match expected %q", iss, v.issuer)
		}
	}
	if v.audience != "" {
		if !claimHasAudience(claims, v.audience) {
			return fmt.Errorf("jwt: audience does not include expected %q", v.audience)
		}
	}
	return nil
}

// validateTimeClaims checks exp and nbf when present. JSON numbers unmarshal
// to float64; exp/nbf are seconds since epoch (JWT NumericDate).
func validateTimeClaims(claims map[string]any, now time.Time) error {
	if v, ok := claims["exp"]; ok {
		if exp, err := asUnixSeconds(v); err == nil {
			if now.Unix() >= exp {
				return errors.New("jwt: token expired")
			}
		}
	}
	if v, ok := claims["nbf"]; ok {
		if nbf, err := asUnixSeconds(v); err == nil {
			if now.Unix() < nbf {
				return errors.New("jwt: token not yet valid")
			}
		}
	}
	return nil
}

func asUnixSeconds(v any) (int64, error) {
	switch n := v.(type) {
	case float64:
		return int64(n), nil
	case int64:
		return n, nil
	case json.Number:
		return n.Int64()
	}
	return 0, fmt.Errorf("jwt: unexpected claim type %T", v)
}

// claimHasAudience handles aud as either a single string or a []any of strings.
func claimHasAudience(claims map[string]any, want string) bool {
	switch aud := claims["aud"].(type) {
	case string:
		return aud == want
	case []any:
		for _, a := range aud {
			if s, ok := a.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// signHS256JWT is a test helper that builds an HS256 JWT signed with secret.
// It is intended only for unit tests; production code validates tokens, it
// does not mint them.
func signHS256JWT(secret string, claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig, nil
}
