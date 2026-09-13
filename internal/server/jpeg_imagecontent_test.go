package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Minimal JFIF JPEG (SOI + APP0 + EOI).
var jpegPayload = []byte{
	0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10, 0x4a, 0x46, 0x49, 0x46, 0x00, 0x01,
	0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xff, 0xd9,
}

func TestReadAttachmentJPEGImageContentWireMIME(t *testing.T) {
	store, root := testutil.FixtureVault(t)
	dir := filepath.Join(root, "network", "diagrams")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string][]byte{
		"shot.jpg":  jpegPayload,
		"shot.jpeg": jpegPayload,
		"shot.png":  pngPayload,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), payload, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	session := connectMCP(t, ts.URL)

	cases := []struct {
		name string
		want string
	}{
		{"shot.jpg", "image/jpeg"},
		{"shot.jpeg", "image/jpeg"},
		{"shot.png", "image/png"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "read_attachment",
				Arguments: map[string]any{"path": "network/diagrams/" + tc.name},
			})
			if err != nil {
				t.Fatalf("read_attachment: %v", err)
			}
			if res.IsError {
				t.Fatalf("tool error: %+v", res.Content)
			}
			if len(res.Content) != 1 {
				t.Fatalf("content = %#v", res.Content)
			}
			img, ok := res.Content[0].(*mcp.ImageContent)
			if !ok {
				t.Fatalf("want ImageContent, got %T %#v", res.Content[0], res.Content[0])
			}
			if img.MIMEType != tc.want {
				t.Fatalf("ImageContent.MIMEType = %q, want %q", img.MIMEType, tc.want)
			}
			wantBytes := jpegPayload
			if strings.HasSuffix(tc.name, ".png") {
				wantBytes = pngPayload
			}
			if !bytes.Equal(img.Data, wantBytes) {
				t.Fatalf("decoded data mismatch (len=%d)", len(img.Data))
			}

			// Marshal the content block the same way the MCP SDK puts it on the wire.
			// Clients silently drop ImageContent when mimeType is "image/jpg" or empty.
			wire, err := json.Marshal(img)
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Type     string `json:"type"`
				MIMEType string `json:"mimeType"`
				Data     string `json:"data"`
			}
			if err := json.Unmarshal(wire, &parsed); err != nil {
				t.Fatal(err)
			}
			if parsed.Type != "image" {
				t.Fatalf("wire type = %q", parsed.Type)
			}
			if parsed.MIMEType != tc.want {
				t.Fatalf("wire mimeType = %q, want %q", parsed.MIMEType, tc.want)
			}
			if parsed.MIMEType == "image/jpg" {
				t.Fatal("invalid MIME image/jpg on wire — clients drop the block")
			}
			if parsed.Data == "" {
				t.Fatal("wire data is empty")
			}

			sc, ok := res.StructuredContent.(map[string]any)
			if !ok {
				t.Fatalf("structured = %#v", res.StructuredContent)
			}
			if got, _ := sc["mime_type"].(string); got != tc.want {
				t.Fatalf("structured mime_type = %q, want %q", got, tc.want)
			}
		})
	}
}
