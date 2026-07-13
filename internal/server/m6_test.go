package server_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var pngPayload = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

func writeFixturePNG(t *testing.T, root string, segments ...string) {
	t.Helper()
	path := filepath.Join(append([]string{root}, segments...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pngPayload, 0o644); err != nil {
		t.Fatal(err)
	}
}

// connectMCP opens an authenticated MCP session against a running server.
func connectMCP(t *testing.T, baseURL string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	httpClient := &http.Client{
		Transport: bearerRoundTripper{token: testToken, base: http.DefaultTransport},
	}
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   baseURL + "/mcp",
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// renderFixture starts a server whose PublicURL is its own base URL, so
// minted Render Links are directly fetchable, and returns an MCP session.
func renderFixture(t *testing.T) (*httptest.Server, *mcp.ClientSession, string) {
	t.Helper()
	store, root := testutil.FixtureVault(t)
	writeFixturePNG(t, root, "Pegasus", "90 Assets", "Photos", "pegasus.png")

	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	handler = server.NewHTTPServer(server.Deps{Store: store, Token: testToken, PublicURL: ts.URL})

	return ts, connectMCP(t, ts.URL), root
}

// mintURL fetches the Render Link for the fixture image via list_attachments.
func mintURL(t *testing.T, session *mcp.ClientSession) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_attachments",
		Arguments: map[string]any{"path_prefix": "Pegasus"},
	})
	if err != nil {
		t.Fatalf("list_attachments: %v", err)
	}
	if res.IsError {
		t.Fatalf("list error: %+v", res.Content)
	}
	atts := res.StructuredContent.(map[string]any)["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %#v", atts)
	}
	u, _ := atts[0].(map[string]any)["url"].(string)
	if u == "" {
		t.Fatalf("attachment has no url: %#v", atts[0])
	}
	return u
}

func TestRenderLinkServesAttachment(t *testing.T) {
	_, session, _ := renderFixture(t)
	minted := mintURL(t, session)
	if strings.Contains(minted, " ") {
		t.Fatalf("minted url contains raw space: %q", minted)
	}

	resp, err := http.Get(minted) // deliberately unauthenticated
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Fatalf("cache-control = %q, want no-store", cc)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(body, pngPayload) {
		t.Fatalf("body = %v", body)
	}
}

func TestRenderLinkURLAlsoOnReadAttachment(t *testing.T) {
	_, session, _ := renderFixture(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "read_attachment",
		Arguments: map[string]any{"path": "Pegasus/90 Assets/Photos/pegasus.png"},
	})
	if err != nil {
		t.Fatalf("read_attachment: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	u, _ := res.StructuredContent.(map[string]any)["url"].(string)
	if !strings.Contains(u, "/render/") {
		t.Fatalf("url = %q", u)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestRenderLinkRejectsTampering(t *testing.T) {
	_, session, _ := renderFixture(t)
	minted := mintURL(t, session)
	u, err := url.Parse(minted)
	if err != nil {
		t.Fatal(err)
	}

	get := func(raw string) int {
		t.Helper()
		resp, err := http.Get(raw)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	// Tampered signature.
	q := u.Query()
	sig := q.Get("sig")
	q.Set("sig", "0"+sig[1:])
	if q.Get("sig") == sig {
		q.Set("sig", "1"+sig[1:])
	}
	tampered := *u
	tampered.RawQuery = q.Encode()
	if code := get(tampered.String()); code != http.StatusForbidden {
		t.Fatalf("tampered sig status = %d, want 403", code)
	}

	// Tampered expiry (signature no longer matches).
	q = u.Query()
	q.Set("exp", "9999999999")
	tampered = *u
	tampered.RawQuery = q.Encode()
	if code := get(tampered.String()); code != http.StatusForbidden {
		t.Fatalf("tampered exp status = %d, want 403", code)
	}

	// Tampered path (valid sig for a different file).
	tampered = *u
	tampered.Path = "/render/network/taxonomy.md"
	if code := get(tampered.String()); code != http.StatusForbidden {
		t.Fatalf("tampered path status = %d, want 403", code)
	}

	// No signature at all.
	tampered = *u
	tampered.RawQuery = ""
	if code := get(tampered.String()); code != http.StatusForbidden {
		t.Fatalf("unsigned status = %d, want 403", code)
	}
}

func TestRenderRouteAbsentWithoutPublicURL(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/render/network/diagrams/wan.png?exp=1&sig=00")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("render route served without PublicURL: %d", resp.StatusCode)
	}
}

func TestNoURLFieldWithoutPublicURL(t *testing.T) {
	store, root := testutil.FixtureVault(t)
	writeFixturePNG(t, root, "network", "diagrams", "wan.png")
	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	// Cleanup, not defer: the session's own cleanup must close before the
	// server, or ts.Close blocks on the client's open MCP connection.
	t.Cleanup(ts.Close)

	session := connectMCP(t, ts.URL)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_attachments",
		Arguments: map[string]any{"path_prefix": "network"},
	})
	if err != nil {
		t.Fatalf("list_attachments: %v", err)
	}
	atts := res.StructuredContent.(map[string]any)["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %#v", atts)
	}
	if u, ok := atts[0].(map[string]any)["url"]; ok {
		t.Fatalf("url must be absent without PublicURL, got %v", u)
	}
}
