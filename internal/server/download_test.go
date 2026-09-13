package server_test

import (
	"bytes"
	"context"
	"io"
	"mime"
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

// videoFixture stands up a server holding one mp4 too large to inline, and
// returns the MCP session plus the vault root.
func videoFixture(t *testing.T, size int) (*httptest.Server, *mcp.ClientSession, []byte) {
	t.Helper()
	store, root := testutil.FixtureVault(t)
	store.MaxAttachmentBytes = 64 // force the "too big to inline" path

	body := bytes.Repeat([]byte{0x00, 0x00, 0x00, 0x18}, size/4)
	path := filepath.Join(root, "network", "clips", "demo.mp4")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	handler = server.NewHTTPServer(server.Deps{Store: store, Token: testToken, PublicURL: ts.URL})

	return ts, connectMCP(t, ts.URL), body
}

// attachmentLinks returns the url / download_url pair a tool minted.
func attachmentLinks(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (string, string, map[string]any) {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s error: %+v", tool, res.Content)
	}
	out := res.StructuredContent.(map[string]any)
	if tool == "list_attachments" {
		atts := out["attachments"].([]any)
		if len(atts) != 1 {
			t.Fatalf("attachments = %#v", atts)
		}
		out = atts[0].(map[string]any)
	}
	u, _ := out["url"].(string)
	d, _ := out["download_url"].(string)
	return u, d, out
}

func TestDownloadURLSavesInsteadOfRendering(t *testing.T) {
	_, session, _ := renderFixture(t)
	u, d, _ := attachmentLinks(t, session, "read_attachment",
		map[string]any{"path": "Pegasus/90 Assets/Photos/pegasus.png"})
	if d == "" {
		t.Fatal("read_attachment returned no download_url")
	}

	for _, tc := range []struct {
		name, url, want string
	}{
		{"render link displays inline", u, "inline"},
		{"download link saves", d, "attachment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			disp, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
			if err != nil {
				t.Fatalf("content-disposition: %v", err)
			}
			if disp != tc.want {
				t.Errorf("disposition = %q, want %q", disp, tc.want)
			}
			// The filename is what the browser's save dialog offers.
			if params["filename"] != "pegasus.png" {
				t.Errorf("filename = %q", params["filename"])
			}
			body, _ := io.ReadAll(resp.Body)
			if !bytes.Equal(body, pngPayload) {
				t.Errorf("body = %v", body)
			}
		})
	}
}

func TestDownloadSupportsRangeRequests(t *testing.T) {
	_, session, body := videoFixture(t, 4096)
	_, d, _ := attachmentLinks(t, session, "list_attachments", map[string]any{"path_prefix": "network/clips"})
	if d == "" {
		t.Fatal("no download_url for the video")
	}

	// A browser <video> element seeks with Range; a resumed download does too.
	req, err := http.NewRequest(http.MethodGet, d, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status = %d, want 206", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Range"); got != "bytes 100-199/4096" {
		t.Errorf("content-range = %q", got)
	}
	part, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(part, body[100:200]) {
		t.Error("ranged bytes do not match the file")
	}

	// The whole file still downloads in one piece.
	whole, err := http.Get(d)
	if err != nil {
		t.Fatal(err)
	}
	defer whole.Body.Close()
	got, _ := io.ReadAll(whole.Body)
	if !bytes.Equal(got, body) {
		t.Errorf("full download = %d bytes, want %d", len(got), len(body))
	}
	if ct := whole.Header.Get("Content-Type"); ct != "video/mp4" {
		t.Errorf("content-type = %q, want video/mp4", ct)
	}
	if cc := whole.Header.Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("cache-control = %q, want no-store", cc)
	}
}

func TestReadAttachmentReturnsLinksForVideo(t *testing.T) {
	_, session, body := videoFixture(t, 4096)

	u, d, out := attachmentLinks(t, session, "read_attachment", map[string]any{"path": "network/clips/demo.mp4"})
	if out["mime_type"] != "video/mp4" {
		t.Errorf("mime_type = %v", out["mime_type"])
	}
	// Video is never base64'd into an MCP response — it answers with links.
	if inlined, _ := out["inlined"].(bool); inlined {
		t.Error("video should not be inlined")
	}
	if out["note"] == nil {
		t.Error("expected a note explaining how to fetch the bytes")
	}
	if u == "" || d == "" {
		t.Fatalf("url = %q, download_url = %q", u, d)
	}
	resp, err := http.Get(d)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, body) {
		t.Error("download_url did not serve the video bytes")
	}
}

func TestReadAttachmentStillInlinesImages(t *testing.T) {
	_, session, _ := renderFixture(t)
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "read_attachment",
		Arguments: map[string]any{"path": "Pegasus/90 Assets/Photos/pegasus.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := res.StructuredContent.(map[string]any)
	if inlined, _ := out["inlined"].(bool); !inlined {
		t.Error("a small image should still be inlined")
	}
	if len(res.Content) == 0 {
		t.Fatal("expected an image content block")
	}
	if _, ok := res.Content[0].(*mcp.ImageContent); !ok {
		t.Errorf("content[0] = %T, want ImageContent", res.Content[0])
	}
}

func TestUploadedVideoIsImmediatelyDownloadable(t *testing.T) {
	ts, session, _ := videoFixture(t, 64)
	clip := bytes.Repeat([]byte{0x21}, 2048)

	if resp := putAttachment(t, ts, "network/clips/new.mp4", clip, testToken, ""); resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status = %d (body %s)", resp.StatusCode, body)
	}
	_, d, out := attachmentLinks(t, session, "read_attachment", map[string]any{"path": "network/clips/new.mp4"})
	if out["size"].(float64) != float64(len(clip)) {
		t.Errorf("size = %v, want %d", out["size"], len(clip))
	}
	resp, err := http.Get(d)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, clip) {
		t.Error("round-tripped video bytes differ")
	}
}
