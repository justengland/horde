package oauth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/justengland/horde/internal/oauth"
)

const testToken = "test-token-123"

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	oauth.New("https://horde.example.com", testToken).Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestDiscoveryEndpoints(t *testing.T) {
	ts := newTestServer(t)
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server",
	} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("%s: status %d", path, res.StatusCode)
		}
		var m map[string]any
		if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
		res.Body.Close()
	}
}

func TestFullAuthorizationCodeFlow(t *testing.T) {
	ts := newTestServer(t)
	redirectURI := "https://claude.ai/api/mcp/auth_callback"

	// Dynamic client registration.
	regBody := `{"redirect_uris":["` + redirectURI + `"],"client_name":"Claude"}`
	res, err := http.Post(ts.URL+"/oauth/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 201 {
		t.Fatalf("register status %d", res.StatusCode)
	}
	var reg struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&reg); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	verifier := "test-verifier-test-verifier-test-verifier-1234"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// Authorize form renders.
	authURL := ts.URL + "/oauth/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {reg.ClientID},
		"redirect_uri":          {redirectURI},
		"state":                 {"st4te"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}.Encode()
	res, err = http.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("authorize form status %d", res.StatusCode)
	}
	res.Body.Close()

	// Submit with the wrong token: rejected.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	form := url.Values{
		"client_id":      {reg.ClientID},
		"redirect_uri":   {redirectURI},
		"state":          {"st4te"},
		"code_challenge": {challenge},
		"token":          {"wrong"},
	}
	res, err = client.PostForm(ts.URL+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("wrong token: status %d, want 401", res.StatusCode)
	}
	res.Body.Close()

	// Submit with the right token: redirect carrying code + state.
	form.Set("token", testToken)
	res, err = client.PostForm(ts.URL+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 302 {
		t.Fatalf("authorize status %d, want 302", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if got := loc.Query().Get("state"); got != "st4te" {
		t.Fatalf("state = %q", got)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("no code in redirect")
	}

	// PKCE mismatch is rejected.
	res, err = http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {reg.ClientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {"not-the-verifier"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 400 {
		t.Fatalf("bad verifier: status %d, want 400", res.StatusCode)
	}
	res.Body.Close()

	// Code is single-use, so a fresh one is needed for the happy path.
	res, err = client.PostForm(ts.URL+"/oauth/authorize", form)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, _ = url.Parse(res.Header.Get("Location"))
	code = loc.Query().Get("code")

	res, err = http.PostForm(ts.URL+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {reg.ClientID},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("token status %d", res.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tok); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if tok.AccessToken != testToken || tok.TokenType != "Bearer" {
		t.Fatalf("token response = %+v", tok)
	}
}

func TestUnknownClientTrustedRedirect(t *testing.T) {
	// After a restart registrations are gone; claude.ai redirect URIs must
	// still be allowed for a never-registered client_id.
	ts := newTestServer(t)
	authURL := ts.URL + "/oauth/authorize?" + url.Values{
		"response_type":  {"code"},
		"client_id":      {"stale-client-id"},
		"redirect_uri":   {"https://claude.ai/api/mcp/auth_callback"},
		"code_challenge": {"x"},
	}.Encode()
	res, err := http.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d, want 200", res.StatusCode)
	}

	// But an arbitrary https redirect for an unknown client is rejected.
	authURL = ts.URL + "/oauth/authorize?" + url.Values{
		"response_type":  {"code"},
		"client_id":      {"stale-client-id"},
		"redirect_uri":   {"https://evil.example.com/cb"},
		"code_challenge": {"x"},
	}.Encode()
	res2, err := http.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != 400 {
		t.Fatalf("status %d, want 400", res2.StatusCode)
	}
}
