package git

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const Author = "horde <mcp@pluto>"

// Repo runs git commands in a vault root.
type Repo struct {
	Root string
	Push bool
}

// CommitStages stages explicit paths and commits. Never stages directories or trees.
func (r *Repo) CommitStages(tool string, relPaths ...string) (string, error) {
	if len(relPaths) == 0 {
		return "", fmt.Errorf("no paths to commit")
	}
	for _, p := range relPaths {
		if err := r.run("add", "--", p); err != nil {
			return "", fmt.Errorf("git add %s: %w", p, err)
		}
	}
	// Rewriting a file with the bytes it already had stages nothing, and
	// `git commit` fails on an empty diff. That is a no-op, not an error:
	// the caller's write succeeded and the vault holds what they asked for.
	staged, err := r.hasStagedChanges(relPaths)
	if err != nil {
		return "", err
	}
	if !staged {
		return r.Head()
	}
	msg := fmt.Sprintf("mcp(%s): %s", tool, relPaths[0])
	if len(relPaths) > 1 {
		msg = fmt.Sprintf("mcp(%s): %s", tool, strings.Join(relPaths, ", "))
	}
	if err := r.run("commit", "-m", msg, "--author", Author); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}
	hash, err := r.Head()
	if err != nil {
		return "", err
	}
	if r.Push {
		go r.pushAsync()
	}
	return hash, nil
}

// RemoveStages git-rms a path and commits. Untracked files are added first so deletion is recoverable.
func (r *Repo) RemoveStages(tool, relPath string) (string, error) {
	tracked, err := r.IsTracked(relPath)
	if err != nil {
		return "", err
	}
	if !tracked {
		if err := r.run("add", "--", relPath); err != nil {
			return "", fmt.Errorf("git add before rm: %w", err)
		}
	}
	if err := r.run("rm", "--", relPath); err != nil {
		return "", fmt.Errorf("git rm: %w", err)
	}
	msg := fmt.Sprintf("mcp(%s): %s", tool, relPath)
	if err := r.run("commit", "-m", msg, "--author", Author); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}
	hash, err := r.Head()
	if err != nil {
		return "", err
	}
	if r.Push {
		go r.pushAsync()
	}
	return hash, nil
}

func (r *Repo) Head() (string, error) {
	out, err := r.output("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsTracked reports whether relPath is in the git index or history.
func (r *Repo) IsTracked(relPath string) (bool, error) {
	out, err := r.output("ls-files", "--error-unmatch", "--", relPath)
	if err != nil {
		if strings.Contains(err.Error(), "exit status 1") {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// StagedFiles returns paths staged for the next commit.
func (r *Repo) StagedFiles() ([]string, error) {
	out, err := r.output("diff", "--cached", "--name-only")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// RecordRename stages a filesystem rename for commit.
func (r *Repo) RecordRename(tool, from, to string, wasTracked bool) (string, error) {
	if wasTracked {
		if err := r.run("add", "--", to); err != nil {
			return "", fmt.Errorf("git add %s: %w", to, err)
		}
		if err := r.run("rm", "--", from); err != nil {
			return "", fmt.Errorf("git rm %s: %w", from, err)
		}
	} else {
		if err := r.run("add", "--", to); err != nil {
			return "", fmt.Errorf("git add %s: %w", to, err)
		}
	}
	msg := fmt.Sprintf("mcp(%s): %s -> %s", tool, from, to)
	if err := r.run("commit", "-m", msg, "--author", Author); err != nil {
		return "", fmt.Errorf("git commit: %w", err)
	}
	hash, err := r.Head()
	if err != nil {
		return "", err
	}
	if r.Push {
		go r.pushAsync()
	}
	return hash, nil
}

func (r *Repo) pushAsync() {
	_ = r.run("push")
}

// hasStagedChanges reports whether any of relPaths differs from HEAD in the
// index. `git diff --cached --quiet` exits 1 when there are differences, which
// is a result rather than a failure — anything else is a real error.
func (r *Repo) hasStagedChanges(relPaths []string) (bool, error) {
	args := append([]string{"diff", "--cached", "--quiet", "--"}, relPaths...)
	cmd := exec.Command("git", append([]string{"-C", r.Root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	if msg := strings.TrimSpace(stderr.String()); msg != "" {
		return false, fmt.Errorf("git diff --cached: %w: %s", err, msg)
	}
	return false, fmt.Errorf("git diff --cached: %w", err)
}

func (r *Repo) run(args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", r.Root}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func (r *Repo) output(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", r.Root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("%w: %s", err, msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
