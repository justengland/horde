package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/git"
)

func TestFileHistoryAndRestore(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	notePath := filepath.Join(root, "network", "note.md")
	if err := os.MkdirAll(filepath.Dir(notePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := &git.Repo{Root: root}
	hash1, err := repo.CommitStages("create_note", "network/note.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(notePath, []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hash2, err := repo.CommitStages("update_note", "network/note.md")
	if err != nil {
		t.Fatal(err)
	}

	history, err := repo.FileHistory("network/note.md", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d", len(history))
	}
	if history[0].Hash != hash2 {
		t.Fatalf("newest = %s want %s", history[0].Hash, hash2)
	}
	if !strings.Contains(history[0].Message, "update_note") {
		t.Fatalf("message = %q", history[0].Message)
	}

	if err := os.WriteFile(notePath, []byte("v3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restoreHash, err := repo.RestoreFile("restore", "network/note.md", hash1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v1\n" {
		t.Fatalf("content = %q", data)
	}
	out, err := exec.Command("git", "-C", root, "show", "--name-only", "--pretty=format:", restoreHash).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "network/note.md") {
		t.Fatalf("restore commit files = %q", out)
	}
}
