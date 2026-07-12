package vault_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/git"
	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/vault"
)

func initGit(t *testing.T, root string) *git.Repo {
	t.Helper()
	if err := exec.Command("git", "-C", root, "init").Run(); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"config", "user.email", "mcp@pluto"},
		{"config", "user.name", "horde"},
	} {
		if err := exec.Command("git", append([]string{"-C", root}, args...)...).Run(); err != nil {
			t.Fatal(err)
		}
	}
	return &git.Repo{Root: root}
}

func writeStore(t *testing.T, root string, g *git.Repo) *vault.Store {
	t.Helper()
	store, err := server.NewStore(root, nil, 2*1024*1024, g, false)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCreateNote(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := initGit(t, root)
	store := writeStore(t, root, g)

	res, err := store.CreateNote("network/new.md", "# hello\n", false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.Commit == "" {
		t.Fatalf("result = %#v", res)
	}
	data, err := os.ReadFile(filepath.Join(root, "network/new.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# hello\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestCreateNoteRejectsExisting(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "network", "x.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, root, nil)
	if _, err := store.CreateNote("network/x.md", "new", false); err == nil {
		t.Fatal("expected exists error")
	}
}

func TestPatchRequiresUniqueOldStr(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "network", "x.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("foo bar foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, root, nil)
	if _, err := store.UpdateNote("network/x.md", "patch", "foo", "baz", ""); err == nil {
		t.Fatal("expected non-unique old_str error")
	}
	res, err := store.UpdateNote("network/x.md", "patch", " bar ", " BAZ ", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.BytesWritten == 0 {
		t.Fatal("expected bytes written")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "foo BAZ foo\n" {
		t.Fatalf("got %q", data)
	}
}

func TestAtomicWriteNoTornFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "network")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "note.md")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := writeStore(t, root, nil)
	if _, err := store.UpdateNote("network/note.md", "replace", "", "", "# replaced\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# replaced\n" {
		t.Fatalf("got %q", data)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("leftover temp files: %v", matches)
	}
}

func TestDeleteNoteGitRm(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "network", "gone.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := initGit(t, root)
	store := writeStore(t, root, g)
	if _, err := store.CreateNote("network/gone.md", "bye\n", true); err != nil {
		t.Fatal(err)
	}
	res, err := store.DeleteNote("network/gone.md")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit == "" || !strings.Contains(res.RestoreHint, "git checkout") {
		t.Fatalf("result = %#v", res)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file should be deleted")
	}
}

func TestReadOnlyRejectsWrite(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := server.NewStore(root, nil, 2*1024*1024, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateNote("network/x.md", "x", false); err == nil {
		t.Fatal("expected read-only error")
	}
}
