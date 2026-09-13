package vault_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/config"
	"github.com/justengland/horde/internal/git"
	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/vault"
)

var testPNG = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// writableAttachmentStore builds a git-backed store with the default
// attachment allowlist, ready to receive uploads.
func writableAttachmentStore(t *testing.T, g *git.Repo, root string) *vault.Store {
	t.Helper()
	store, err := server.NewStore(server.StoreOpts{
		Root:               root,
		MaxNoteBytes:       2 * 1024 * 1024,
		MaxAttachmentBytes: config.DefaultMaxAttachmentBytes,
		AttachmentMIME:     mapsCopy(config.KnownAttachmentMIME),
		Git:                g,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func attachmentFixture(t *testing.T) (*vault.Store, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := initGit(t, root)
	return writableAttachmentStore(t, g, root), root
}

func TestWriteAttachmentCreatesAndCommits(t *testing.T) {
	store, root := attachmentFixture(t)

	res, err := store.WriteAttachment("network/diagrams/wan.png", testPNG, false)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !res.Created {
		t.Error("expected Created true for a new attachment")
	}
	if res.BytesWritten != int64(len(testPNG)) {
		t.Errorf("bytes_written = %d, want %d", res.BytesWritten, len(testPNG))
	}
	if res.Commit == "" {
		t.Error("expected a commit hash")
	}
	got, err := os.ReadFile(filepath.Join(root, "network/diagrams/wan.png"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(testPNG) {
		t.Error("round-tripped bytes differ")
	}
	// The upload must be readable through the normal read path.
	att, err := store.ReadAttachment("network/diagrams/wan.png")
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if att.MIMEType != "image/png" {
		t.Errorf("mime = %q, want image/png", att.MIMEType)
	}
	out, err := exec.Command("git", "-C", root, "log", "--oneline", "-1").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "create_attachment") {
		t.Errorf("commit message = %q, want create_attachment", out)
	}
}

func TestWriteAttachmentRejects(t *testing.T) {
	store, _ := attachmentFixture(t)

	cases := []struct {
		name string
		path string
		data []byte
		want string
	}{
		{"disallowed extension", "network/notes/evil.sh", testPNG, "allowlisted"},
		{"markdown is not an attachment", "network/notes/page.md", testPNG, "allowlisted"},
		{"outside a wiki", "loose.png", testPNG, "inside a wiki"},
		{"traversal", "network/../../escape.png", testPNG, ""},
		{"excluded dir", ".obsidian/theme.png", testPNG, ""},
		{"empty content", "network/diagrams/empty.png", nil, "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.WriteAttachment(tc.path, tc.data, false)
			if err == nil {
				t.Fatalf("expected error for %q", tc.path)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A leading slash is normalized away (the same rule ReadAttachment uses for
// wiki:// URIs), so an absolute-looking path writes inside the vault, never
// at the filesystem location it names.
func TestWriteAttachmentConfinesAbsolutePaths(t *testing.T) {
	store, root := attachmentFixture(t)

	res, err := store.WriteAttachment("/network/diagrams/wan.png", testPNG, false)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if res.Path != "network/diagrams/wan.png" {
		t.Errorf("path = %q, want the leading slash stripped", res.Path)
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); err != nil {
		t.Errorf("expected the write inside the vault: %v", err)
	}
}

func TestWriteAttachmentSizeCap(t *testing.T) {
	store, _ := attachmentFixture(t)
	store.MaxAttachmentBytes = 16

	if _, err := store.WriteAttachment("network/diagrams/big.png", make([]byte, 17), false); err == nil {
		t.Fatal("expected an over-cap write to fail")
	} else if !strings.Contains(err.Error(), "exceeds cap") {
		t.Errorf("error = %q, want it to mention the cap", err)
	}
	if _, err := store.WriteAttachment("network/diagrams/ok.png", make([]byte, 16), false); err != nil {
		t.Errorf("at-cap write failed: %v", err)
	}
}

func TestWriteAttachmentOverwrite(t *testing.T) {
	store, _ := attachmentFixture(t)
	if _, err := store.WriteAttachment("network/diagrams/wan.png", testPNG, false); err != nil {
		t.Fatal(err)
	}

	if _, err := store.WriteAttachment("network/diagrams/wan.png", testPNG, false); err == nil {
		t.Fatal("expected a second write without overwrite to fail")
	}
	res, err := store.WriteAttachment("network/diagrams/wan.png", append(testPNG, 0x42), true)
	if err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if res.Created {
		t.Error("expected Created false when overwriting")
	}
}

func TestWriteAttachmentAcceptsWikiURI(t *testing.T) {
	store, root := attachmentFixture(t)
	if _, err := store.WriteAttachment("wiki://network/diagrams/wan.png", testPNG, false); err != nil {
		t.Fatalf("write via URI: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); err != nil {
		t.Errorf("expected the file at the decoded path: %v", err)
	}
}

func TestDeleteAttachment(t *testing.T) {
	store, root := attachmentFixture(t)
	if _, err := store.WriteAttachment("network/diagrams/wan.png", testPNG, false); err != nil {
		t.Fatal(err)
	}

	res, err := store.DeleteAttachment("network/diagrams/wan.png")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !res.Deleted || res.Commit == "" || res.RestoreHint == "" {
		t.Errorf("unexpected delete result %+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); !os.IsNotExist(err) {
		t.Error("expected the file to be gone")
	}
	if _, err := store.DeleteAttachment("network/diagrams/wan.png"); err == nil {
		t.Error("expected deleting a missing attachment to fail")
	}
	if _, err := store.DeleteAttachment("network/opnsense/wan-failover.md"); err == nil {
		t.Error("expected delete_attachment to refuse a markdown path")
	}
}

func TestMoveAttachment(t *testing.T) {
	store, root := attachmentFixture(t)
	if _, err := store.WriteAttachment("network/inbox/wan.png", testPNG, false); err != nil {
		t.Fatal(err)
	}

	res, err := store.MoveAttachment("network/inbox/wan.png", "network/diagrams/wan.png", false)
	if err != nil {
		t.Fatalf("move: %v", err)
	}
	if res.Commit == "" {
		t.Error("expected a commit hash")
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); err != nil {
		t.Errorf("expected the file at the destination: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "network/inbox/wan.png")); !os.IsNotExist(err) {
		t.Error("expected the source to be gone")
	}
	if _, err := store.MoveAttachment("network/diagrams/wan.png", "network/diagrams/wan.exe", false); err == nil {
		t.Error("expected a move to a disallowed extension to fail")
	}
}

func TestAttachmentWritesRefusedWhenReadOnly(t *testing.T) {
	store, _ := attachmentFixture(t)
	if _, err := store.WriteAttachment("network/diagrams/wan.png", testPNG, false); err != nil {
		t.Fatal(err)
	}
	store.ReadOnly = true

	if _, err := store.WriteAttachment("network/diagrams/other.png", testPNG, false); err == nil {
		t.Error("expected write to fail when read-only")
	}
	if _, err := store.DeleteAttachment("network/diagrams/wan.png"); err == nil {
		t.Error("expected delete to fail when read-only")
	}
	if _, err := store.MoveAttachment("network/diagrams/wan.png", "network/diagrams/moved.png", false); err == nil {
		t.Error("expected move to fail when read-only")
	}
}
