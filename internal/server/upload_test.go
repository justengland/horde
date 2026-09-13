package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
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

// uploadFixture starts a writable server whose PublicURL is its own base URL,
// so minted Render Links on freshly uploaded attachments are fetchable.
func uploadFixture(t *testing.T) (*httptest.Server, *mcp.ClientSession, string) {
	t.Helper()
	store, root := testutil.FixtureVault(t)

	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	handler = server.NewHTTPServer(server.Deps{Store: store, Token: testToken, PublicURL: ts.URL})

	return ts, connectMCP(t, ts.URL), root
}

func putAttachment(t *testing.T, ts *httptest.Server, path string, body []byte, token, query string) *http.Response {
	t.Helper()
	url := ts.URL + "/attachments/" + path
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestUploadRouteStoresAttachment(t *testing.T) {
	ts, session, root := uploadFixture(t)

	resp := putAttachment(t, ts, "network/diagrams/wan.png", pngPayload, testToken, "")
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
	var out struct {
		Path         string `json:"path"`
		URI          string `json:"uri"`
		URL          string `json:"url"`
		DownloadURL  string `json:"download_url"`
		BytesWritten int64  `json:"bytes_written"`
		Created      bool   `json:"created"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Path != "network/diagrams/wan.png" || !out.Created {
		t.Errorf("unexpected response %+v", out)
	}
	if out.URI != "wiki://network/diagrams/wan.png" {
		t.Errorf("uri = %q", out.URI)
	}
	if out.BytesWritten != int64(len(pngPayload)) {
		t.Errorf("bytes_written = %d", out.BytesWritten)
	}
	on, err := os.ReadFile(filepath.Join(root, "network/diagrams/wan.png"))
	if err != nil || !bytes.Equal(on, pngPayload) {
		t.Fatalf("on-disk bytes = %v, err = %v", on, err)
	}

	// The Render Link minted for the upload serves the same bytes, and the
	// upload response hands back a save-to-disk link too.
	if !strings.Contains(out.URL, "/render/") {
		t.Fatalf("url = %q", out.URL)
	}
	if !strings.Contains(out.DownloadURL, "download=1") {
		t.Errorf("download_url = %q", out.DownloadURL)
	}
	rendered, err := http.Get(out.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer rendered.Body.Close()
	got, _ := io.ReadAll(rendered.Body)
	if !bytes.Equal(got, pngPayload) {
		t.Errorf("rendered body = %v", got)
	}

	// And it is immediately discoverable through the read surface.
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "list_attachments",
		Arguments: map[string]any{"path_prefix": "network"},
	})
	if err != nil {
		t.Fatal(err)
	}
	atts := res.StructuredContent.(map[string]any)["attachments"].([]any)
	if len(atts) != 1 {
		t.Fatalf("attachments = %#v", atts)
	}
}

func TestUploadRouteRequiresBearer(t *testing.T) {
	ts, _, root := uploadFixture(t)

	resp := putAttachment(t, ts, "network/diagrams/wan.png", pngPayload, "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	resp = putAttachment(t, ts, "network/diagrams/wan.png", pngPayload, "wrong-token", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); !os.IsNotExist(err) {
		t.Error("an unauthorized upload wrote to the vault")
	}
}

func TestUploadRouteRejections(t *testing.T) {
	ts, _, _ := uploadFixture(t)
	if resp := putAttachment(t, ts, "network/diagrams/wan.png", pngPayload, testToken, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup status = %d", resp.StatusCode)
	}

	cases := []struct {
		name  string
		path  string
		body  []byte
		query string
		want  int
	}{
		{"existing file without overwrite", "network/diagrams/wan.png", pngPayload, "", http.StatusConflict},
		{"disallowed extension", "network/diagrams/evil.sh", pngPayload, "", http.StatusBadRequest},
		{"markdown", "network/diagrams/page.md", pngPayload, "", http.StatusBadRequest},
		{"outside a wiki", "loose.png", pngPayload, "", http.StatusBadRequest},
		{"empty body", "network/diagrams/empty.png", nil, "", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := putAttachment(t, ts, tc.path, tc.body, testToken, tc.query)
			if resp.StatusCode != tc.want {
				body, _ := io.ReadAll(resp.Body)
				t.Errorf("status = %d, want %d (body %s)", resp.StatusCode, tc.want, body)
			}
		})
	}

	// overwrite=true turns the conflict into a successful update.
	resp := putAttachment(t, ts, "network/diagrams/wan.png", append(pngPayload, 0x42), testToken, "overwrite=true")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("overwrite status = %d, want 200", resp.StatusCode)
	}
}

// The upload route streams, so it carries the larger HORDE_MAX_STREAM_BYTES
// cap rather than the in-memory HORDE_MAX_ATTACHMENT_BYTES one — a video is
// uploadable even though it could never fit in an MCP response.
func TestUploadRouteUsesStreamCap(t *testing.T) {
	store, root := testutil.FixtureVault(t)
	store.MaxAttachmentBytes = 8
	store.MaxStreamBytes = 1024
	ts := httptest.NewServer(server.NewHTTPServer(server.Deps{Store: store, Token: testToken}))
	t.Cleanup(ts.Close)

	// Past the inline cap but inside the stream cap: accepted.
	if resp := putAttachment(t, ts, "network/clips/demo.mp4", make([]byte, 512), testToken, ""); resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201 (body %s)", resp.StatusCode, body)
	}
	if info, err := os.Stat(filepath.Join(root, "network/clips/demo.mp4")); err != nil || info.Size() != 512 {
		t.Fatalf("on-disk size = %v, err = %v", info, err)
	}

	// Past the stream cap: rejected, and no partial file left behind.
	resp := putAttachment(t, ts, "network/clips/big.mp4", make([]byte, 2048), testToken, "")
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "network/clips/big.mp4")); !os.IsNotExist(err) {
		t.Error("an over-cap upload left a file behind")
	}
	entries, err := os.ReadDir(filepath.Join(root, "network/clips"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file %q left behind", e.Name())
		}
	}
}

func TestUploadRouteAbsentWhenReadOnly(t *testing.T) {
	store, root := testutil.FixtureVault(t)
	store.ReadOnly = true
	ts := httptest.NewServer(server.NewHTTPServer(server.Deps{Store: store, Token: testToken}))
	t.Cleanup(ts.Close)

	resp := putAttachment(t, ts, "network/diagrams/wan.png", pngPayload, testToken, "")
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want the route to be absent", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); !os.IsNotExist(err) {
		t.Error("a read-only server wrote to the vault")
	}
}

func TestCreateAttachmentTool(t *testing.T) {
	_, session, root := uploadFixture(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_attachment",
		Arguments: map[string]any{
			"path":           "network/diagrams/wan.png",
			"content_base64": base64.StdEncoding.EncodeToString(pngPayload),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	out := res.StructuredContent.(map[string]any)
	if out["path"] != "network/diagrams/wan.png" {
		t.Errorf("path = %v", out["path"])
	}
	if u, _ := out["url"].(string); !strings.Contains(u, "/render/") {
		t.Errorf("url = %q", u)
	}
	on, err := os.ReadFile(filepath.Join(root, "network/diagrams/wan.png"))
	if err != nil || !bytes.Equal(on, pngPayload) {
		t.Fatalf("on-disk bytes = %v, err = %v", on, err)
	}
}

func TestCreateAttachmentToolRejectsBadBase64(t *testing.T) {
	_, session, root := uploadFixture(t)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_attachment",
		Arguments: map[string]any{
			"path":           "network/diagrams/wan.png",
			"content_base64": "not!valid!base64",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected a tool error for invalid base64")
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); !os.IsNotExist(err) {
		t.Error("invalid base64 still wrote a file")
	}
}

func TestDeleteAndMoveAttachmentTools(t *testing.T) {
	_, session, root := uploadFixture(t)
	writeFixturePNG(t, root, "network", "inbox", "wan.png")

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "move_attachment",
		Arguments: map[string]any{"from": "network/inbox/wan.png", "to": "network/diagrams/wan.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("move error: %+v", res.Content)
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); err != nil {
		t.Fatalf("expected the moved file: %v", err)
	}

	// delete without confirm must be refused.
	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "delete_attachment",
		Arguments: map[string]any{"path": "network/diagrams/wan.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected delete without confirm to fail")
	}

	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "delete_attachment",
		Arguments: map[string]any{"path": "network/diagrams/wan.png", "confirm": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("delete error: %+v", res.Content)
	}
	if _, err := os.Stat(filepath.Join(root, "network/diagrams/wan.png")); !os.IsNotExist(err) {
		t.Error("expected the attachment to be gone")
	}
}

func TestAttachmentWriteToolsHiddenWhenReadOnly(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
	store.ReadOnly = true
	ts := httptest.NewServer(server.NewHTTPServer(server.Deps{Store: store, Token: testToken}))
	t.Cleanup(ts.Close)
	session := connectMCP(t, ts.URL)

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		switch tool.Name {
		case "create_attachment", "delete_attachment", "move_attachment":
			t.Errorf("%s is exposed on a read-only server", tool.Name)
		}
	}
}
