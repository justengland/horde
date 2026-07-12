package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justengland/horde/internal/paths"
)

type WriteResult struct {
	Path         string `json:"path"`
	Commit       string `json:"commit,omitempty"`
	BytesWritten int64  `json:"bytes_written,omitempty"`
	Created      bool   `json:"created,omitempty"`
}

type DeleteResult struct {
	Path        string `json:"path"`
	Deleted     bool   `json:"deleted"`
	Commit      string `json:"commit,omitempty"`
	RestoreHint string `json:"restore_hint,omitempty"`
}

type MoveResult struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Commit string `json:"commit,omitempty"`
}

func (s *Store) checkWritable() error {
	if s.ReadOnly {
		return fmt.Errorf("server is read-only")
	}
	return nil
}

func (s *Store) validateWritePath(apiPath string) error {
	if err := paths.ValidateAPIPath(apiPath); err != nil {
		return err
	}
	if !s.Paths.IsNotePath(apiPath) {
		return fmt.Errorf("notes must live inside a wiki")
	}
	if !paths.AllowedWriteExt(apiPath) {
		return fmt.Errorf("only .md files may be written")
	}
	return nil
}

func (s *Store) checkSize(n int64) error {
	if n > s.MaxNoteBytes {
		return fmt.Errorf("content exceeds max note size (%d bytes)", s.MaxNoteBytes)
	}
	return nil
}

// CreateNote writes a new note and optionally commits it.
func (s *Store) CreateNote(apiPath, content string, overwrite bool) (*WriteResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if err := s.validateWritePath(apiPath); err != nil {
		return nil, err
	}
	if err := s.checkSize(int64(len(content))); err != nil {
		return nil, err
	}
	abs, err := s.Paths.Resolve(apiPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err == nil {
		if !overwrite {
			return nil, fmt.Errorf("file already exists")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	if err := atomicWriteFile(abs, []byte(content), 0o644); err != nil {
		return nil, err
	}
	out := &WriteResult{
		Path:         apiPath,
		BytesWritten: int64(len(content)),
		Created:      true,
	}
	if s.Git != nil {
		out.Commit, err = s.Git.CommitStages("create_note", apiPath)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateNote modifies a note using patch, append, prepend, or replace mode.
func (s *Store) UpdateNote(apiPath, mode, oldStr, newStr, content string) (*WriteResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if err := s.validateWritePath(apiPath); err != nil {
		return nil, err
	}
	abs, err := s.Paths.Resolve(apiPath)
	if err != nil {
		return nil, err
	}
	existing, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	updated, err := applyUpdate(string(existing), mode, oldStr, newStr, content)
	if err != nil {
		return nil, err
	}
	if err := s.checkSize(int64(len(updated))); err != nil {
		return nil, err
	}
	if err := atomicWriteFile(abs, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	out := &WriteResult{
		Path:         apiPath,
		BytesWritten: int64(len(updated)),
	}
	if s.Git != nil {
		out.Commit, err = s.Git.CommitStages("update_note", apiPath)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func applyUpdate(existing, mode, oldStr, newStr, content string) (string, error) {
	switch mode {
	case "patch":
		if oldStr == "" {
			return "", fmt.Errorf("old_str is required for patch mode")
		}
		count := strings.Count(existing, oldStr)
		if count != 1 {
			return "", fmt.Errorf("old_str must appear exactly once (found %d)", count)
		}
		return strings.Replace(existing, oldStr, newStr, 1), nil
	case "append":
		if content == "" {
			return "", fmt.Errorf("content is required for append mode")
		}
		return existing + content, nil
	case "prepend":
		if content == "" {
			return "", fmt.Errorf("content is required for prepend mode")
		}
		return content + existing, nil
	case "replace":
		if content == "" {
			return "", fmt.Errorf("content is required for replace mode")
		}
		return content, nil
	default:
		return "", fmt.Errorf("unknown mode %q", mode)
	}
}

// DeleteNote removes a note via git rm when git is enabled.
func (s *Store) DeleteNote(apiPath string) (*DeleteResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if err := s.validateWritePath(apiPath); err != nil {
		return nil, err
	}
	abs, err := s.Paths.Resolve(apiPath)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, err
	}
	out := &DeleteResult{Path: apiPath, Deleted: true}
	if s.Git != nil {
		hash, err := s.Git.RemoveStages("delete_note", apiPath)
		if err != nil {
			return nil, err
		}
		out.Commit = hash
		out.RestoreHint = fmt.Sprintf("git checkout %s^ -- %s", hash, apiPath)
		return out, nil
	}
	if err := os.Remove(abs); err != nil {
		return nil, err
	}
	return out, nil
}

// MoveNote renames a note within the vault.
func (s *Store) MoveNote(from, to string, overwrite bool) (*MoveResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if err := s.validateWritePath(from); err != nil {
		return nil, err
	}
	if err := s.validateWritePath(to); err != nil {
		return nil, err
	}
	fromAbs, err := s.Paths.Resolve(from)
	if err != nil {
		return nil, err
	}
	toAbs, err := s.Paths.Resolve(to)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(fromAbs); err != nil {
		return nil, err
	}
	if _, err := os.Stat(toAbs); err == nil {
		if !overwrite {
			return nil, fmt.Errorf("destination already exists")
		}
		if err := os.Remove(toAbs); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(toAbs), 0o755); err != nil {
		return nil, err
	}
	var wasTracked bool
	if s.Git != nil {
		wasTracked, err = s.Git.IsTracked(from)
		if err != nil {
			return nil, err
		}
	}
	if err := os.Rename(fromAbs, toAbs); err != nil {
		return nil, err
	}
	out := &MoveResult{From: from, To: to}
	if s.Git != nil {
		out.Commit, err = s.Git.RecordRename("move_note", from, to, wasTracked)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d", base, time.Now().UnixNano()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
