package vault_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLinkGraphPagesOrphansAndBroken(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "network/opnsense"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"network/index.md": `# Index
See [[wan-failover]] and [[missing-page]].
Also [[opnsense/wan-failover]].
`,
		"network/opnsense/wan-failover.md": `# WAN
Back to [[index]].
`,
		"network/orphan.md": `# Lonely
No inbound links.
`,
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := attachmentStore(t, root)
	g, err := store.LinkGraph("network")
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Pages) != 3 {
		t.Fatalf("pages = %#v", g.Pages)
	}
	byPath := map[string]struct {
		out []string
		in  int
	}{}
	for _, p := range g.Pages {
		byPath[p.Path] = struct {
			out []string
			in  int
		}{p.OutLinks, p.InLinkCount}
	}
	idx := byPath["network/index.md"]
	if !slices.Contains(idx.out, "wan-failover") || !slices.Contains(idx.out, "missing-page") {
		t.Fatalf("index out_links = %#v", idx.out)
	}
	if byPath["network/opnsense/wan-failover.md"].in < 1 {
		t.Fatalf("wan-failover in_link_count = %d", byPath["network/opnsense/wan-failover.md"].in)
	}
	if !slices.Contains(g.Orphans, "network/orphan.md") {
		t.Fatalf("orphans = %#v", g.Orphans)
	}
	foundBroken := false
	for _, b := range g.BrokenLinks {
		if b.Path == "network/index.md" && b.Target == "missing-page" {
			foundBroken = true
		}
	}
	if !foundBroken {
		t.Fatalf("broken_links = %#v", g.BrokenLinks)
	}
}
