package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/vault"
)

func FixtureVault(t *testing.T) (*vault.Store, string) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"network/opnsense", "recipes", ".git", ".obsidian"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"network/taxonomy.md":              "# network taxonomy\n",
		"network/opnsense/wan-failover.md": "# WAN Failover\n\nDeco LED fix details.\n",
		"recipes/index.md":                 "# Recipes\n",
		"README-at-root.md":                "loose root file\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
	store, err := server.NewStore(root, nil, 2*1024*1024, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}
