package git_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/git"
)

func initGitRepo(t *testing.T, root string) {
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
}

func TestCommitStagesOnlyExplicitPaths(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)

	bystander := filepath.Join(root, "network", "bystander.md")
	target := filepath.Join(root, "network", "new.md")
	if err := os.MkdirAll(filepath.Dir(bystander), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bystander, []byte("human edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repo := &git.Repo{Root: root}
	if _, err := repo.CommitStages("create_note", "network/new.md"); err != nil {
		t.Fatal(err)
	}

	staged, err := repo.StagedFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("expected clean index after commit, staged: %v", staged)
	}

	out, err := exec.Command("git", "-C", root, "show", "--name-only", "--pretty=format:", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	if !contains(files, "network/new.md") {
		t.Fatalf("commit should include new.md, got: %v", files)
	}
	if contains(files, "network/bystander.md") {
		t.Fatalf("commit must not include bystander, got: %v", files)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// Rewriting a file with the bytes it already had stages nothing. git commit
// fails on an empty diff, but the caller's write did succeed, so the commit
// is a no-op that returns the unchanged HEAD rather than an error.
func TestCommitStagesNoOpOnUnchangedContent(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	repo := &git.Repo{Root: root}
	path := filepath.Join(root, "network", "note.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := repo.CommitStages("create_note", "network/note.md")
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if first == "" {
		t.Fatal("expected a commit hash")
	}

	// Same content again: no new commit, no error, HEAD unmoved.
	if err := os.WriteFile(path, []byte("same bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := repo.CommitStages("update_note", "network/note.md")
	if err != nil {
		t.Fatalf("re-commit of identical content: %v", err)
	}
	if second != first {
		t.Errorf("HEAD moved from %s to %s on an empty diff", first, second)
	}

	// A real change still commits.
	if err := os.WriteFile(path, []byte("different bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := repo.CommitStages("update_note", "network/note.md")
	if err != nil {
		t.Fatalf("third commit: %v", err)
	}
	if third == first {
		t.Error("expected a new commit for changed content")
	}
}

// Gitignored paths (vault binaries, backed up outside git) are written to disk
// but never committed; write, move, and delete all succeed without git.
func TestIgnoredPathsSkipGit(t *testing.T) {
	root := t.TempDir()
	initGitRepo(t, root)
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "*.png\n")
	write("a.png", "png")
	repo := &git.Repo{Root: root}
	if _, err := repo.CommitStages("create_note", ".gitignore"); err != nil {
		t.Fatal(err)
	}
	head, _ := repo.Head()

	if hash, err := repo.CommitStages("create_attachment", "a.png"); err != nil || hash != "" {
		t.Fatalf("create: hash=%q err=%v", hash, err)
	}
	if err := os.Rename(filepath.Join(root, "a.png"), filepath.Join(root, "b.png")); err != nil {
		t.Fatal(err)
	}
	if hash, err := repo.RecordRename("move_attachment", "a.png", "b.png", false); err != nil || hash != "" {
		t.Fatalf("move: hash=%q err=%v", hash, err)
	}
	if hash, err := repo.RemoveStages("delete_attachment", "b.png"); err != nil || hash != "" {
		t.Fatalf("delete: hash=%q err=%v", hash, err)
	}
	if _, err := os.Stat(filepath.Join(root, "b.png")); !os.IsNotExist(err) {
		t.Fatalf("b.png should be gone, stat err=%v", err)
	}
	if now, _ := repo.Head(); now != head {
		t.Fatalf("ignored paths must not commit: %s -> %s", head, now)
	}
}
