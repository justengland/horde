package vault_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/testutil"
	"github.com/justengland/horde/internal/vault"
)

func TestListWikis(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
	wikis, err := store.ListWikis()
	if err != nil {
		t.Fatal(err)
	}
	if len(wikis) != 2 {
		t.Fatalf("got %d wikis", len(wikis))
	}
	var networkFound bool
	for _, w := range wikis {
		if w.Name == "network" {
			networkFound = true
			if !w.HasTaxonomy {
				t.Fatal("network should have taxonomy")
			}
			if w.NoteCount < 1 {
				t.Fatal("expected notes in network wiki")
			}
		}
	}
	if !networkFound {
		t.Fatal("network wiki missing")
	}
}

func TestReadNoteFrontmatter(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "recipes"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := mustStore(t, root)
	path := "recipes/cake.md"
	content := "---\ntitle: Cake\n---\n\nBody text\n"
	if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	note, err := store.ReadNote(path, true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if note.Body != "Body text\n" {
		t.Fatalf("body = %q", note.Body)
	}
	if note.Frontmatter["title"] != "Cake" {
		t.Fatalf("frontmatter = %#v", note.Frontmatter)
	}
}

func TestSearchNotesAND(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
	hits, err := store.SearchNotes(vault.SearchParams{Query: "Deco LED", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %#v", hits)
	}
	if hits[0].Path != "network/opnsense/wan-failover.md" {
		t.Fatalf("path = %q", hits[0].Path)
	}
}

func TestReadNoteOffset(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "recipes"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := mustStore(t, root)
	path := "recipes/long.md"
	body := strings.Repeat("abcdefghij", 12000) // 120k
	if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	note, err := store.ReadNote(path, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !note.Truncated {
		t.Fatal("expected truncated")
	}
	if len(note.Body) != int(vault.DefaultReadLimit) {
		t.Fatalf("body len = %d", len(note.Body))
	}
	page, err := store.ReadNote(path, false, vault.DefaultReadLimit, 100)
	if err != nil {
		t.Fatal(err)
	}
	if page.Offset != vault.DefaultReadLimit {
		t.Fatalf("offset = %d", page.Offset)
	}
	if len(page.Body) != 100 {
		t.Fatalf("body len = %d", len(page.Body))
	}
}

func TestUpdateNoteRejectsConflictMarkers(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	store := mustStore(t, root)
	path := "network/conflicted.md"
	content := "<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> branch\n"
	if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.UpdateNote(path, "append", "", "", "\nfix\n")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "conflict markers") {
		t.Fatalf("err = %v", err)
	}
}

func mustStore(t *testing.T, root string) *vault.Store {
	t.Helper()
	store, err := server.NewStore(root, nil, 2*1024*1024, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
