package paths_test

import (
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
