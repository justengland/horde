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
