package render

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testSigner(t *testing.T) *Signer {
	t.Helper()
	s := NewSigner("https://horde.example.com")
	if len(s.secret) < 32 {
		t.Fatalf("secret length = %d, want >= 32", len(s.secret))
	}
	return s
}

// mintParts parses a minted URL into its decoded path and query values.
func mintParts(t *testing.T, minted string) (string, url.Values) {
	t.Helper()
	u, err := url.Parse(minted)
	if err != nil {
		t.Fatalf("parse %q: %v", minted, err)
	}
	const prefix = "/render/"
	if !strings.HasPrefix(u.Path, prefix) {
		t.Fatalf("path %q does not start with %s", u.Path, prefix)
	}
	return strings.TrimPrefix(u.Path, prefix), u.Query()
}

func TestMintVerifyRoundTrip(t *testing.T) {
	s := testSigner(t)
	minted := s.Mint("network/diagrams/wan.png")
	if !strings.HasPrefix(minted, "https://horde.example.com/render/") {
		t.Fatalf("minted = %q", minted)
	}
	path, q := mintParts(t, minted)
	if path != "network/diagrams/wan.png" {
		t.Fatalf("decoded path = %q", path)
	}
	if err := s.Verify(path, q.Get("exp"), q.Get("sig")); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestMintEncodesSpaces(t *testing.T) {
	s := testSigner(t)
	minted := s.Mint("Pegasus/90 Assets/Photos/pegasus.png")
	if strings.Contains(minted, " ") {
		t.Fatalf("minted URL contains a raw space: %q", minted)
	}
	path, q := mintParts(t, minted)
	if path != "Pegasus/90 Assets/Photos/pegasus.png" {
		t.Fatalf("decoded path = %q", path)
	}
	if err := s.Verify(path, q.Get("exp"), q.Get("sig")); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRejectsTamperedPath(t *testing.T) {
	s := testSigner(t)
	_, q := mintParts(t, s.Mint("network/diagrams/wan.png"))
	err := s.Verify("network/secret/other.png", q.Get("exp"), q.Get("sig"))
	if err == nil || errors.Is(err, ErrExpired) {
		t.Fatalf("want signature error, got %v", err)
	}
}

func TestVerifyRejectsTamperedExpiry(t *testing.T) {
	s := testSigner(t)
	path, q := mintParts(t, s.Mint("network/diagrams/wan.png"))
	if err := s.Verify(path, "9999999999", q.Get("sig")); err == nil {
		t.Fatal("want error for tampered exp")
	}
}

func TestVerifyRejectsTamperedSignature(t *testing.T) {
	s := testSigner(t)
	path, q := mintParts(t, s.Mint("network/diagrams/wan.png"))
	sig := q.Get("sig")
	flipped := "0" + sig[1:]
	if flipped == sig {
		flipped = "1" + sig[1:]
	}
	if err := s.Verify(path, q.Get("exp"), flipped); err == nil {
		t.Fatal("want error for tampered sig")
	}
	if err := s.Verify(path, q.Get("exp"), "not-hex!!"); err == nil {
		t.Fatal("want error for garbage sig")
	}
	if err := s.Verify(path, q.Get("exp"), ""); err == nil {
		t.Fatal("want error for empty sig")
	}
}

func TestVerifyRejectsExpiredLink(t *testing.T) {
	s := testSigner(t)
	path, q := mintParts(t, s.Mint("network/diagrams/wan.png"))

	s.now = func() time.Time { return time.Now().Add(TTL + time.Minute) }
	err := s.Verify(path, q.Get("exp"), q.Get("sig"))
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

func TestRestartRevokesLinks(t *testing.T) {
	// A new Signer (fresh boot secret) must reject links minted by the old one.
	old := testSigner(t)
	path, q := mintParts(t, old.Mint("network/diagrams/wan.png"))
	fresh := NewSigner("https://horde.example.com")
	if err := fresh.Verify(path, q.Get("exp"), q.Get("sig")); err == nil {
		t.Fatal("want error after secret rotation")
	}
}

func TestTTLIsAboutSixHours(t *testing.T) {
	s := testSigner(t)
	_, q := mintParts(t, s.Mint("network/diagrams/wan.png"))
	exp, err := strconv.ParseInt(q.Get("exp"), 10, 64)
	if err != nil {
		t.Fatalf("exp %q: %v", q.Get("exp"), err)
	}
	got := time.Until(time.Unix(exp, 0))
	if got < 5*time.Hour || got > 7*time.Hour {
		t.Fatalf("ttl = %v, want ~6h", got)
	}
}
