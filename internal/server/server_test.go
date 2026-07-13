package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justengland/horde/internal/server"
	"github.com/justengland/horde/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "test-token-secret-value-32bytes!!"

func TestHealthz(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestMCPRequiresBearerAuth(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
	handler := server.NewHTTPServer(server.Deps{Store: store, Token: testToken})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/mcp", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d want 401", resp.StatusCode)
	}
}

func TestHelloViaMCP(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
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
		Name:      "hello",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type %T", res.StructuredContent)
	}
	if m["message"] != "hello from horde" {
		t.Fatalf("message = %#v", m["message"])
	}
}

func TestListWikisViaMCP(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
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
		Name:      "list_wikis",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content type %T", res.StructuredContent)
	}
	wikis, ok := m["wikis"].([]any)
	if !ok || len(wikis) != 2 {
		t.Fatalf("wikis = %#v", m["wikis"])
	}
}

func TestSearchNotesViaMCP(t *testing.T) {
	store, _ := testutil.FixtureVault(t)
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
		Name: "search_notes",
		Arguments: map[string]any{
			"query": "Deco LED",
		},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				t.Fatalf("tool error: %s", tc.Text)
			}
		}
		t.Fatalf("tool error: %+v", res.Content)
	}
	m := res.StructuredContent.(map[string]any)
	results := m["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(req)
}
