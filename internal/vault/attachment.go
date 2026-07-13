package vault

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/justengland/horde/internal/paths"
)

type AttachmentMeta struct {
	URI      string    `json:"uri"`
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	MIMEType string    `json:"mime_type"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type AttachmentRead struct {
	URI      string `json:"uri"`
	Path     string `json:"path"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
	Bytes    []byte `json:"-"`
}

// ListAttachments enumerates allowlisted attachment files under prefix.
func (s *Store) ListAttachments(prefix string, limit int) ([]AttachmentMeta, error) {
	if limit <= 0 {
		limit = 500
	}
	base, err := s.Paths.ResolveDir(prefix)
	if err != nil {
		return nil, err
	}
	var out []AttachmentMeta
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != base && s.Paths.IsExcludedName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := s.Paths.ToRelative(path)
		if err != nil {
			return err
		}
		if !s.Paths.IsNotePath(rel) {
			return nil
		}
		mime, ok := s.attachmentMIME(rel)
		if !ok {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, AttachmentMeta{
			URI:      paths.WikiURI(rel),
			Path:     rel,
			Name:     filepath.Base(rel),
			MIMEType: mime,
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
		if len(out) >= limit {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil && err != fs.SkipAll {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ReadAttachment returns raw bytes for an allowlisted attachment.
// pathOrURI may be a root-relative path or a wiki:// URI.
func (s *Store) ReadAttachment(pathOrURI string) (*AttachmentRead, error) {
	apiPath, err := paths.ParseWikiURI(pathOrURI)
	if err != nil {
		return nil, err
	}
	if err := paths.ValidateAPIPath(apiPath); err != nil {
		return nil, err
	}
	if !s.Paths.IsNotePath(apiPath) {
		return nil, fmt.Errorf("attachments must live inside a wiki")
	}
	mime, ok := s.attachmentMIME(apiPath)
	if !ok {
		return nil, fmt.Errorf("extension is not an allowlisted attachment type")
	}
	abs, err := s.Paths.Resolve(apiPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	if info.Size() > s.MaxAttachmentBytes {
		return nil, fmt.Errorf("attachment %s size %d exceeds cap %d bytes", apiPath, info.Size(), s.MaxAttachmentBytes)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return &AttachmentRead{
		URI:      paths.WikiURI(apiPath),
		Path:     apiPath,
		MIMEType: mime,
		Size:     info.Size(),
		Bytes:    data,
	}, nil
}

func (s *Store) attachmentMIME(apiPath string) (string, bool) {
	if s.AttachmentMIME == nil {
		return "", false
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(apiPath), "."))
	mime, ok := s.AttachmentMIME[ext]
	return mime, ok
}
