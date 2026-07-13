package config_test

import (
	"testing"

	"github.com/justengland/horde/internal/config"
)

func TestLoadAttachmentDefaults(t *testing.T) {
	t.Setenv("HORDE_TOKEN", "test-token-value-for-config")
	t.Setenv("HORDE_ROOT", t.TempDir())
	t.Setenv("HORDE_MAX_ATTACHMENT_BYTES", "")
	t.Setenv("HORDE_ATTACHMENT_EXTS", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxAttachmentBytes != config.DefaultMaxAttachmentBytes {
		t.Fatalf("MaxAttachmentBytes = %d", cfg.MaxAttachmentBytes)
	}
	want := []string{"png", "jpg", "jpeg", "gif", "webp", "pdf"}
	if len(cfg.AttachmentExts) != len(want) {
		t.Fatalf("AttachmentExts = %#v", cfg.AttachmentExts)
	}
	for i, ext := range want {
		if cfg.AttachmentExts[i] != ext {
			t.Fatalf("AttachmentExts[%d] = %q", i, cfg.AttachmentExts[i])
		}
	}
	if cfg.AttachmentMIME["png"] != "image/png" {
		t.Fatalf("png mime = %q", cfg.AttachmentMIME["png"])
	}
	if cfg.AttachmentMIME["pdf"] != "application/pdf" {
		t.Fatalf("pdf mime = %q", cfg.AttachmentMIME["pdf"])
	}
}

func TestLoadAttachmentExtsFiltersUnknown(t *testing.T) {
	t.Setenv("HORDE_TOKEN", "test-token-value-for-config")
	t.Setenv("HORDE_ROOT", t.TempDir())
	t.Setenv("HORDE_ATTACHMENT_EXTS", "png,tiff,pdf")
	t.Setenv("HORDE_MAX_ATTACHMENT_BYTES", "1048576")

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxAttachmentBytes != 1048576 {
		t.Fatalf("MaxAttachmentBytes = %d", cfg.MaxAttachmentBytes)
	}
	// tiff has no MIME map entry — dropped from effective allowlist
	if len(cfg.AttachmentExts) != 2 || cfg.AttachmentExts[0] != "png" || cfg.AttachmentExts[1] != "pdf" {
		t.Fatalf("AttachmentExts = %#v", cfg.AttachmentExts)
	}
	if _, ok := cfg.AttachmentMIME["tiff"]; ok {
		t.Fatal("tiff should not have MIME entry")
	}
}
