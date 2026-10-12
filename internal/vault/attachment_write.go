package vault

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/justengland/horde/internal/paths"
)

// validateAttachmentWritePath enforces the same rules ReadAttachment enforces:
// a valid root-relative path inside a wiki whose extension is on the
// configured allowlist. Uploads never widen the read surface.
func (s *Store) validateAttachmentWritePath(apiPath string) (string, error) {
	if err := paths.ValidateAPIPath(apiPath); err != nil {
		return "", err
	}
	if !s.Paths.IsNotePath(apiPath) {
		return "", fmt.Errorf("attachments must live inside a wiki")
	}
	mime, ok := s.attachmentMIME(apiPath)
	if !ok {
		return "", fmt.Errorf("extension is not an allowlisted attachment type")
	}
	return mime, nil
}

// WriteAttachment stores raw bytes at apiPath and commits the result.
// pathOrURI may be a root-relative path or a wiki:// URI. Bytes already in
// memory are capped at MaxAttachmentBytes — the in-memory limit; use
// WriteAttachmentStream for anything arriving over the wire.
func (s *Store) WriteAttachment(pathOrURI string, data []byte, overwrite bool) (*WriteResult, error) {
	if int64(len(data)) > s.MaxAttachmentBytes {
		return nil, fmt.Errorf("attachment size %d exceeds cap %d bytes", len(data), s.MaxAttachmentBytes)
	}
	return s.writeAttachment(pathOrURI, bytes.NewReader(data), s.MaxAttachmentBytes, overwrite)
}

// WriteAttachmentStream stores an attachment straight from r without holding
// it in memory, so video-sized uploads stay bounded by disk, not RAM. It is
// capped at MaxStreamBytes.
func (s *Store) WriteAttachmentStream(pathOrURI string, r io.Reader, overwrite bool) (*WriteResult, error) {
	return s.writeAttachment(pathOrURI, r, s.streamCap(), overwrite)
}

func (s *Store) writeAttachment(pathOrURI string, r io.Reader, limit int64, overwrite bool) (*WriteResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	apiPath, err := paths.ParseWikiURI(pathOrURI)
	if err != nil {
		return nil, err
	}
	if _, err := s.validateAttachmentWritePath(apiPath); err != nil {
		return nil, err
	}
	abs, err := s.Paths.Resolve(apiPath)
	if err != nil {
		return nil, err
	}
	created := true
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("path is a directory")
		}
		if !overwrite {
			return nil, fmt.Errorf("file already exists")
		}
		created = false
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	n, err := streamToFile(abs, r, limit)
	if err != nil {
		return nil, err
	}
	out := &WriteResult{
		Path:         apiPath,
		BytesWritten: n,
		Created:      created,
	}
	if s.Git != nil {
		out.Commit, err = s.Git.CommitStages("create_attachment", apiPath)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// streamToFile writes r into a temp file beside path and renames it into
// place, so a truncated or over-cap upload never leaves a partial attachment
// where Obsidian or the read surface can see it. It reads one byte past cap to
// tell "exactly at the cap" from "over it".
func streamToFile(path string, r io.Reader, limit int64) (int64, error) {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d", base, time.Now().UnixNano()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	cleanup := func() {
		f.Close()
		os.Remove(tmp)
	}
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if err != nil {
		cleanup()
		return 0, err
	}
	if n > limit {
		cleanup()
		return 0, fmt.Errorf("attachment exceeds cap %d bytes", limit)
	}
	if n == 0 {
		cleanup()
		return 0, fmt.Errorf("attachment content is empty")
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return 0, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return 0, err
	}
	return n, nil
}

// DeleteAttachment removes an allowlisted attachment (git-recoverable).
func (s *Store) DeleteAttachment(pathOrURI string) (*DeleteResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	apiPath, err := paths.ParseWikiURI(pathOrURI)
	if err != nil {
		return nil, err
	}
	if _, err := s.validateAttachmentWritePath(apiPath); err != nil {
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
		hash, err := s.Git.RemoveStages("delete_attachment", apiPath)
		if err != nil {
			return nil, err
		}
		out.Commit = hash
		if hash != "" {
			out.RestoreHint = fmt.Sprintf("git checkout %s^ -- %s", hash, apiPath)
		}
		return out, nil
	}
	if err := os.Remove(abs); err != nil {
		return nil, err
	}
	return out, nil
}

// MoveAttachment renames an attachment within the vault. Like move_note it
// does not rewrite the ![[embeds]] that point at it.
func (s *Store) MoveAttachment(from, to string, overwrite bool) (*MoveResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	fromPath, err := paths.ParseWikiURI(from)
	if err != nil {
		return nil, err
	}
	toPath, err := paths.ParseWikiURI(to)
	if err != nil {
		return nil, err
	}
	if _, err := s.validateAttachmentWritePath(fromPath); err != nil {
		return nil, err
	}
	if _, err := s.validateAttachmentWritePath(toPath); err != nil {
		return nil, err
	}
	fromAbs, err := s.Paths.Resolve(fromPath)
	if err != nil {
		return nil, err
	}
	toAbs, err := s.Paths.Resolve(toPath)
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
		wasTracked, err = s.Git.IsTracked(fromPath)
		if err != nil {
			return nil, err
		}
	}
	if err := os.Rename(fromAbs, toAbs); err != nil {
		return nil, err
	}
	out := &MoveResult{From: fromPath, To: toPath}
	if s.Git != nil {
		out.Commit, err = s.Git.RecordRename("move_attachment", fromPath, toPath, wasTracked)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
