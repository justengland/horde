package vault_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/config"
	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/vault"
)

func attachmentStore(t *testing.T, root string) *vault.Store {
	t.Helper()
	store, err := server.NewStore(server.StoreOpts{
		Root:               root,
		MaxNoteBytes:       2 * 1024 * 1024,
		MaxAttachmentBytes: config.DefaultMaxAttachmentBytes,
		AttachmentMIME:     mapsCopy(config.KnownAttachmentMIME),
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mapsCopy(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestListAttachmentsFindsAllowlisted(t *testing.T) {
	root := t.TempDir()
	dirs := []string{"network/diagrams", "network/.obsidian", "recipes"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{
		"network/diagrams/wan.png":    {0x89, 0x50, 0x4e, 0x47},
		"network/diagrams/notes.md":   []byte("# notes\n"),
		"network/diagrams/secret.bin": {0x00, 0x01},
		"network/.obsidian/app.png":   {0x89, 0x50, 0x4e, 0x47},
		"recipes/card.pdf":            []byte("%PDF-1.4"),
	}
	for rel, data := range files {
		if err := os.WriteFile(filepath.Join(root, rel), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := attachmentStore(t, root)
	atts, err := store.ListAttachments("network", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 {
		t.Fatalf("atts = %#v", atts)
	}
	if atts[0].Path != "network/diagrams/wan.png" {
		t.Fatalf("path = %q", atts[0].Path)
	}
	if atts[0].URI != "wiki://network/diagrams/wan.png" {
		t.Fatalf("uri = %q", atts[0].URI)
	}
	if atts[0].MIMEType != "image/png" {
		t.Fatalf("mime = %q", atts[0].MIMEType)
	}
	if atts[0].Name != "wan.png" {
		t.Fatalf("name = %q", atts[0].Name)
	}
}

func TestReadAttachmentReturnsBytesAndMIME(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network/diagrams"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a}
	path := "network/diagrams/wan.png"
	if err := os.WriteFile(filepath.Join(root, path), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	store := attachmentStore(t, root)
	att, err := store.ReadAttachment(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(att.Bytes, payload) {
		t.Fatalf("bytes = %v", att.Bytes)
	}
	if att.MIMEType != "image/png" {
		t.Fatalf("mime = %q", att.MIMEType)
	}
	if att.URI != "wiki://network/diagrams/wan.png" {
		t.Fatalf("uri = %q", att.URI)
	}
}

func TestReadAttachmentViaWikiURI(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network/diagrams"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("%PDF-1.4 fake")
	if err := os.WriteFile(filepath.Join(root, "network/diagrams/card.pdf"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	store := attachmentStore(t, root)
	att, err := store.ReadAttachment("wiki://network/diagrams/card.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if att.MIMEType != "application/pdf" {
		t.Fatalf("mime = %q", att.MIMEType)
	}
	if !bytes.Equal(att.Bytes, payload) {
		t.Fatalf("bytes mismatch")
	}
}

func TestReadAttachmentRejectsDisallowedExt(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "network/x.bin"), []byte{1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	store := attachmentStore(t, root)
	_, err := store.ReadAttachment("network/x.bin")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not an allowlisted") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadAttachmentRejectsOversize(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "network/big.png"
	if err := os.WriteFile(filepath.Join(root, path), bytes.Repeat([]byte{0xab}, 100), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := server.NewStore(server.StoreOpts{
		Root:               root,
		MaxNoteBytes:       2 * 1024 * 1024,
		MaxAttachmentBytes: 50,
		AttachmentMIME:     mapsCopy(config.KnownAttachmentMIME),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReadAttachment(path)
	if err == nil {
		t.Fatal("expected oversize error")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "50") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadAttachmentRejectsExcludedDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network/.obsidian"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "network/.obsidian/x.png"), []byte{1}, 0o644); err != nil {
		t.Fatal(err)
	}
	store := attachmentStore(t, root)
	_, err := store.ReadAttachment("network/.obsidian/x.png")
	if err == nil {
		t.Fatal("expected excluded error")
	}
}
