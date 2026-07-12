package paths_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/justengland/horde/internal/paths"
)

func TestResolveRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	v, err := paths.NewVault(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"../etc/passwd", "..", "wiki/../../etc/passwd"} {
		if _, err := v.Resolve(p); err == nil {
			t.Fatalf("Resolve(%q) should fail", p)
		}
	}
}

func TestResolveRejectsAbsolutePath(t *testing.T) {
	v, err := paths.NewVault(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Resolve("/etc/passwd"); err == nil {
		t.Fatal("expected absolute path rejection")
	}
}

func TestResolveRejectsExcludedDirs(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".git", ".obsidian", ".trash", ".claude"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	v, err := paths.NewVault(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".git/config", ".obsidian/workspace", ".trash/note.md", ".claude/settings.md", "wiki/.git/x"} {
		if _, err := v.Resolve(p); err == nil {
			t.Fatalf("Resolve(%q) should fail", p)
		}
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	wiki := filepath.Join(root, "network")
	if err := os.MkdirAll(wiki, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(wiki, "link.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	v, err := paths.NewVault(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Resolve("network/link.md"); err == nil {
		t.Fatal("symlink escape should be rejected")
	}
}

func TestResolveValidNote(t *testing.T) {
	root := t.TempDir()
	note := filepath.Join(root, "network", "note.md")
	if err := os.MkdirAll(filepath.Dir(note), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(note, []byte("# hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	v, err := paths.NewVault(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.Resolve("network/note.md")
	if err != nil {
		t.Fatal(err)
	}
	if got != note {
		t.Fatalf("got %q want %q", got, note)
	}
}
