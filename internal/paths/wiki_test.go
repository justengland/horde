package paths_test

import (
	"strings"
	"testing"

	"github.com/justengland/horde/internal/paths"
)

func TestWikiURIRoundTrip(t *testing.T) {
	uri := paths.WikiURI("network/diagrams/wan.png")
	if uri != "wiki://network/diagrams/wan.png" {
		t.Fatalf("uri = %q", uri)
	}
	p, err := paths.ParseWikiURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if p != "network/diagrams/wan.png" {
		t.Fatalf("path = %q", p)
	}
}

func TestWikiURIPercentEncodesSpaces(t *testing.T) {
	// MCP resource templates reject unencoded spaces; Obsidian vaults use them heavily.
	uri := paths.WikiURI("Pegasus/90 Assets/Photos/pegasus.png")
	want := "wiki://Pegasus/90%20Assets/Photos/pegasus.png"
	if uri != want {
		t.Fatalf("uri = %q, want %q", uri, want)
	}
	p, err := paths.ParseWikiURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if p != "Pegasus/90 Assets/Photos/pegasus.png" {
		t.Fatalf("path = %q", p)
	}
}

func TestWikiURIRoundTripSpecialChars(t *testing.T) {
	api := "Pegasus/06 Reference/Ownership & Survey/hull.jpeg"
	uri := paths.WikiURI(api)
	if strings.Contains(uri, " ") {
		t.Fatalf("uri still has raw spaces: %q", uri)
	}
	p, err := paths.ParseWikiURI(uri)
	if err != nil {
		t.Fatal(err)
	}
	if p != api {
		t.Fatalf("path = %q, want %q", p, api)
	}
}

func TestParseWikiURIPlainPath(t *testing.T) {
	p, err := paths.ParseWikiURI("network/shot.png")
	if err != nil {
		t.Fatal(err)
	}
	if p != "network/shot.png" {
		t.Fatalf("path = %q", p)
	}
}

func TestParseWikiURIRejectsOtherSchemes(t *testing.T) {
	_, err := paths.ParseWikiURI("file:///etc/passwd")
	if err == nil {
		t.Fatal("expected error")
	}
}
