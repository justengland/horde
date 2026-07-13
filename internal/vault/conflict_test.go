package vault_test

import (
	"testing"

	"github.com/justengland/horde/internal/vault"
)

func TestHasConflictMarkers(t *testing.T) {
	if vault.HasConflictMarkers("clean note\n") {
		t.Fatal("expected false")
	}
	conflicted := "before\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> branch\nafter\n"
	if !vault.HasConflictMarkers(conflicted) {
		t.Fatal("expected true")
	}
}
