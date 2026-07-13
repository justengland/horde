package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultAddr         = "127.0.0.1:8787"
	DefaultRoot         = "~/Documents/Obsidian"
	DefaultMaxNoteBytes = 2 * 1024 * 1024
	DefaultReadLimit    = 100 * 1024
)

type Config struct {
	Root           string
	Token          string
	Addr           string
	PublicURL      string
	ReadOnly       bool
	GitEnabled     bool
	GitPush        bool
	Ignore         []string
	MaxNoteBytes   int64
	ReadLimitBytes int64
}

func Load() (Config, error) {
	root := expandHome(envOr("HORDE_ROOT", DefaultRoot))
	token := os.Getenv("HORDE_TOKEN")
	if token == "" {
		return Config{}, fmt.Errorf("HORDE_TOKEN is required")
	}
	gitMode := strings.ToLower(envOr("HORDE_GIT", "on"))
	cfg := Config{
		Root:           root,
		Token:          token,
		Addr:           envOr("HORDE_ADDR", DefaultAddr),
		PublicURL:      strings.TrimRight(os.Getenv("HORDE_PUBLIC_URL"), "/"),
		ReadOnly:       envBool("HORDE_READONLY", false),
		GitEnabled:     gitMode != "off",
		GitPush:        envBool("HORDE_GIT_PUSH", false),
		MaxNoteBytes:   envInt64("HORDE_MAX_NOTE_BYTES", DefaultMaxNoteBytes),
		ReadLimitBytes: DefaultReadLimit,
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
