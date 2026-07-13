// Package render mints and verifies Render Links (ADR-10): HMAC-signed,
// short-lived URLs that let a chat client display an attachment inline.
// The signing secret is generated at boot, so a restart revokes every
// outstanding link; there is deliberately no config surface.
package render

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TTL is how long a minted link stays valid (~6 h per ADR-10, fixed).
const TTL = 6 * time.Hour

var ErrExpired = errors.New("render link expired")

type Signer struct {
	publicURL string
	secret    []byte
	now       func() time.Time
}

// NewSigner creates a Signer with a fresh boot-generated secret.
// publicURL is the external base URL (no trailing slash).
func NewSigner(publicURL string) *Signer {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("render: cannot generate signing secret: %v", err))
	}
	return &Signer{
		publicURL: strings.TrimRight(publicURL, "/"),
		secret:    secret,
		now:       time.Now,
	}
}

// Mint returns a signed URL for one root-relative attachment path:
// {publicURL}/render/{path}?exp=<unix>&sig=<hmac-sha256 hex>.
func (s *Signer) Mint(apiPath string) string {
	exp := strconv.FormatInt(s.now().Add(TTL).Unix(), 10)
	parts := strings.Split(apiPath, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return fmt.Sprintf("%s/render/%s?exp=%s&sig=%s",
		s.publicURL, strings.Join(parts, "/"), exp, hex.EncodeToString(s.sign(apiPath, exp)))
}

// Verify checks the signature and expiry for a decoded root-relative path.
// Returns ErrExpired for a well-signed but stale link.
func (s *Signer) Verify(apiPath, exp, sig string) error {
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) == 0 {
		return errors.New("malformed signature")
	}
	if !hmac.Equal(got, s.sign(apiPath, exp)) {
		return errors.New("invalid signature")
	}
	expUnix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil {
		return errors.New("malformed expiry")
	}
	if s.now().After(time.Unix(expUnix, 0)) {
		return ErrExpired
	}
	return nil
}

// sign length-prefixes each field so no (path, exp) pair shares a MAC
// input with any other — unambiguity must not depend on later parsing.
func (s *Signer) sign(apiPath, exp string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(mac, "%d:%s%d:%s", len(apiPath), apiPath, len(exp), exp)
	return mac.Sum(nil)
}
