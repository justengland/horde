package server_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestListAttachmentsAndReadResourceViaMCP(t *testing.T) {
	store, root := testutil.FixtureVault(t)
	dir := filepath.Join(root, "network", "diagrams")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(filepath.Join(dir, "wan.png"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	httpClient := &http.Client{
		Transport: bearerRoundTripper{token: testToken, base: http.DefaultTransport},
	}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	listRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_attachments",
		Arguments: map[string]any{"path_prefix": "network"},
	})
	if err != nil {
		t.Fatalf("list_attachments: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list error: %+v", listRes.Content)
	}
	m := listRes.StructuredContent.(map[string]any)
	atts := m["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %#v", atts)
	}

	readRes, err := session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: "wiki://network/diagrams/wan.png",
	})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(readRes.Contents) != 1 {
		t.Fatalf("contents = %#v", readRes.Contents)
	}
	c := readRes.Contents[0]
	if c.MIMEType != "image/png" {
		t.Fatalf("mime = %q", c.MIMEType)
	}
	if !bytes.Equal(c.Blob, payload) {
		t.Fatalf("blob mismatch")
	}
}

func TestReadResourceWithSpacedPath(t *testing.T) {
	store, root := testutil.FixtureVault(t)
	dir := filepath.Join(root, "Pegasus", "90 Assets", "Photos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if err := os.WriteFile(filepath.Join(dir, "pegasus.png"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	httpClient := &http.Client{
		Transport: bearerRoundTripper{token: testToken, base: http.DefaultTransport},
	}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	listRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "list_attachments",
		Arguments: map[string]any{"path_prefix": "Pegasus", "limit": 10},
	})
	if err != nil {
		t.Fatalf("list_attachments: %v", err)
	}
	if listRes.IsError {
		t.Fatalf("list error: %+v", listRes.Content)
	}
	m := listRes.StructuredContent.(map[string]any)
	atts := m["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %#v", atts)
	}
	att := atts[0].(map[string]any)
	uri, _ := att["uri"].(string)
	if uri != "wiki://Pegasus/90%20Assets/Photos/pegasus.png" {
		t.Fatalf("listed uri = %q", uri)
	}
	if strings.Contains(uri, " ") {
		t.Fatalf("listed uri must be percent-encoded, got %q", uri)
	}

	readRes, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		t.Fatalf("ReadResource(%q): %v", uri, err)
	}
	if len(readRes.Contents) != 1 || !bytes.Equal(readRes.Contents[0].Blob, payload) {
		t.Fatalf("contents = %#v", readRes.Contents)
	}

	toolRes, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_attachment",
		Arguments: map[string]any{"path": "Pegasus/90 Assets/Photos/pegasus.png"},
	})
	if err != nil {
		t.Fatalf("read_attachment: %v", err)
	}
	if toolRes.IsError {
		t.Fatalf("read_attachment error: %+v", toolRes.Content)
	}
	if len(toolRes.Content) != 1 {
		t.Fatalf("content = %#v", toolRes.Content)
	}
	img, ok := toolRes.Content[0].(*mcp.ImageContent)
	if !ok {
		t.Fatalf("want ImageContent, got %T %#v", toolRes.Content[0], toolRes.Content[0])
	}
	if img.MIMEType != "image/png" || !bytes.Equal(img.Data, payload) {
		t.Fatalf("image = mime=%q len=%d", img.MIMEType, len(img.Data))
	}
}

func TestLinkGraphViaMCP(t *testing.T) {
	store, root := testutil.FixtureVault(t)
	// Fixture already has network notes; add a wikilink.
	path := filepath.Join(root, "network", "taxonomy.md")
	if err := os.WriteFile(path, []byte("# tax\n\nSee [[opnsense/wan-failover]].\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	httpClient := &http.Client{
		Transport: bearerRoundTripper{token: testToken, base: http.DefaultTransport},
	}
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "link_graph",
		Arguments: map[string]any{"wiki": "network"},
	})
	if err != nil {
		t.Fatalf("link_graph: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	m := res.StructuredContent.(map[string]any)
	pages := m["pages"].([]any)
	if len(pages) < 2 {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestNoPublicAttachmentHTTPRoute(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/files/network/diagrams/wan.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("unexpected public file route status %d", resp.StatusCode)
	}
}
