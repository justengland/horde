package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultAddr               = "127.0.0.1:8787"
	DefaultRoot               = "~/Documents/Obsidian"
	DefaultMaxNoteBytes       = 2 * 1024 * 1024
	DefaultMaxAttachmentBytes = 5 * 1024 * 1024
	DefaultMaxStreamBytes     = 512 * 1024 * 1024
	DefaultReadLimit          = 100 * 1024
	DefaultAttachmentExts     = "png,jpg,jpeg,gif,webp,pdf,mp4,webm,mov,m4v"
)

// KnownAttachmentMIME maps extension (no dot) → MIME. Configured
// HORDE_ATTACHMENT_EXTS is intersected with this map; unknown exts are dropped.
//
// jpg/jpeg MUST be "image/jpeg" (never "image/jpg") — image/jpg is not a valid
// MIME type and MCP clients silently drop ImageContent blocks that use it.
var KnownAttachmentMIME = map[string]string{
	"png":  "image/png",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
	"pdf":  "application/pdf",
	"mp4":  "video/mp4",
	"m4v":  "video/mp4",
	"webm": "video/webm",
	"mov":  "video/quicktime",
}

// IsStreamOnlyMIME reports whether a type is too large-by-nature to inline in
// an MCP response. Video is fetched through the download route instead, which
// streams and supports Range requests.
func IsStreamOnlyMIME(mime string) bool {
	return strings.HasPrefix(mime, "video/")
}

type Config struct {
	Root               string
	Token              string
	Addr               string
	PublicURL          string
	ReadOnly           bool
	GitEnabled         bool
	GitPush            bool
	Ignore             []string
	MaxNoteBytes       int64
	MaxAttachmentBytes int64
	MaxStreamBytes     int64
	AttachmentExts     []string          // effective allowlist (no dots)
	AttachmentMIME     map[string]string // ext → MIME for effective allowlist
	ReadLimitBytes     int64
}

func Load() (Config, error) {
	root := expandHome(envOr("HORDE_ROOT", DefaultRoot))
	token := os.Getenv("HORDE_TOKEN")
	if token == "" {
		return Config{}, fmt.Errorf("HORDE_TOKEN is required")
	}
	gitMode := strings.ToLower(envOr("HORDE_GIT", "on"))
	exts, mime := parseAttachmentExts(envOr("HORDE_ATTACHMENT_EXTS", DefaultAttachmentExts))
	cfg := Config{
		Root:               root,
		Token:              token,
		Addr:               envOr("HORDE_ADDR", DefaultAddr),
		PublicURL:          strings.TrimRight(os.Getenv("HORDE_PUBLIC_URL"), "/"),
		ReadOnly:           envBool("HORDE_READONLY", false),
		GitEnabled:         gitMode != "off",
		GitPush:            envBool("HORDE_GIT_PUSH", false),
		MaxNoteBytes:       envInt64("HORDE_MAX_NOTE_BYTES", DefaultMaxNoteBytes),
		MaxAttachmentBytes: envInt64("HORDE_MAX_ATTACHMENT_BYTES", DefaultMaxAttachmentBytes),
		MaxStreamBytes:     envInt64("HORDE_MAX_STREAM_BYTES", DefaultMaxStreamBytes),
		AttachmentExts:     exts,
		AttachmentMIME:     mime,
		ReadLimitBytes:     DefaultReadLimit,
	}
	if v := os.Getenv("HORDE_IGNORE"); v != "" {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				cfg.Ignore = append(cfg.Ignore, part)
			}
		}
	}
	return cfg, nil
}

func parseAttachmentExts(raw string) ([]string, map[string]string) {
	var exts []string
	mime := make(map[string]string)
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		ext := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(part, ".")))
		if ext == "" {
			continue
		}
		m, ok := KnownAttachmentMIME[ext]
		if !ok {
			continue
		}
		if _, dup := seen[ext]; dup {
			continue
		}
		seen[ext] = struct{}{}
		exts = append(exts, ext)
		mime[ext] = m
	}
	return exts, mime
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt64(key string, def int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}
