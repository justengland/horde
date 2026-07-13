// Package oauth implements the minimal single-user OAuth 2.1 flow from
// horde-spec §4's fallback: claude.ai custom connectors require the OAuth
// handshake (discovery, dynamic registration, authorization code + PKCE).
// The "login" step is pasting the horde bearer token, and the issued access
// token is that same token, so the resource-side bearer middleware is
// unchanged and no token state survives restarts.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const codeTTL = 5 * time.Minute

// Redirect URIs trusted even when the client_id is unknown, so a server
// restart (which wipes in-memory registrations) does not strand an
// already-connected claude.ai client mid-reauth.
var trustedRedirectHosts = map[string]bool{
	"claude.ai":  true,
	"claude.com": true,
}

type Server struct {
	PublicURL string // external base URL, no trailing slash
	Token     string // HORDE_TOKEN; validated at authorize, returned by token endpoint

	mu      sync.Mutex
	clients map[string][]string
	codes   map[string]authCode
}

type authCode struct {
	clientID    string
	redirectURI string
	challenge   string
	expires     time.Time
}

func New(publicURL, token string) *Server {
	return &Server{
		PublicURL: strings.TrimRight(publicURL, "/"),
		Token:     token,
		clients:   map[string][]string{},
		codes:     map[string]authCode{},
	}
}

// ResourceMetadataURL is advertised in WWW-Authenticate on 401s.
func (s *Server) ResourceMetadataURL() string {
	return s.PublicURL + "/.well-known/oauth-protected-resource/mcp"
}

func (s *Server) Register(mux *http.ServeMux) {
	// RFC 9728 allows the well-known path with or without the resource's
	// path suffix; serve both.
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/", s.protectedResource)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.asMetadata)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server/", s.asMetadata)
	mux.HandleFunc("POST /oauth/register", s.register)
	mux.HandleFunc("GET /oauth/authorize", s.authorizeForm)
	mux.HandleFunc("POST /oauth/authorize", s.authorizeSubmit)
	mux.HandleFunc("POST /oauth/token", s.token)
}

func (s *Server) protectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.PublicURL + "/mcp",
		"authorization_servers":    []string{s.PublicURL},
		"bearer_methods_supported": []string{"header"},
	})
}

func (s *Server) asMetadata(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.PublicURL,
		"authorization_endpoint":                s.PublicURL + "/oauth/authorize",
		"token_endpoint":                        s.PublicURL + "/oauth/token",
		"registration_endpoint":                 s.PublicURL + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RedirectURIs []string `json:"redirect_uris"`
		ClientName   string   `json:"client_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "bad JSON")
		return
	}
	if len(req.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris required")
		return
	}
	for _, u := range req.RedirectURIs {
		if !validRedirectURI(u) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect URIs must be https (or localhost)")
			return
		}
	}
	clientID := randToken()
	s.mu.Lock()
	s.clients[clientID] = req.RedirectURIs
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"redirect_uris":              req.RedirectURIs,
		"client_name":                req.ClientName,
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code"},
		"response_types":             []string{"code"},
	})
}

var authorizeTmpl = template.Must(template.New("authorize").Parse(`<!doctype html>
<title>horde — authorize</title>
<style>body{font-family:sans-serif;max-width:26rem;margin:15vh auto;padding:0 1rem}
input[type=password]{width:100%;padding:.5rem;margin:.6rem 0}button{padding:.5rem 1.4rem}
.err{color:#b00}</style>
<h2>horde</h2>
<p><b>{{.ClientName}}</b> is requesting access to your vault. Paste the horde token to approve.</p>
{{if .Error}}<p class=err>{{.Error}}</p>{{end}}
<form method=post action=/oauth/authorize>
<input type=hidden name=client_id value="{{.ClientID}}">
<input type=hidden name=redirect_uri value="{{.RedirectURI}}">
<input type=hidden name=state value="{{.State}}">
<input type=hidden name=code_challenge value="{{.Challenge}}">
<input type=password name=token placeholder="horde token" autofocus>
<button type=submit>Approve</button>
</form>`))

type authorizePage struct {
	ClientName, ClientID, RedirectURI, State, Challenge, Error string
}

func (s *Server) checkAuthorizeParams(q url.Values) (authorizePage, error) {
	p := authorizePage{
		ClientID:    q.Get("client_id"),
		RedirectURI: q.Get("redirect_uri"),
		State:       q.Get("state"),
		Challenge:   q.Get("code_challenge"),
		ClientName:  "An MCP client",
	}
	if rt := q.Get("response_type"); rt != "" && rt != "code" {
		return p, fmt.Errorf("unsupported response_type")
	}
	if m := q.Get("code_challenge_method"); m != "" && m != "S256" {
		return p, fmt.Errorf("only S256 code_challenge_method is supported")
	}
	if p.ClientID == "" || p.RedirectURI == "" || p.Challenge == "" {
		return p, fmt.Errorf("client_id, redirect_uri and code_challenge are required")
	}
	if !s.redirectAllowed(p.ClientID, p.RedirectURI) {
		return p, fmt.Errorf("unknown client or redirect_uri")
	}
	return p, nil
}

func (s *Server) redirectAllowed(clientID, redirectURI string) bool {
	if !validRedirectURI(redirectURI) {
		return false
	}
	s.mu.Lock()
	uris, known := s.clients[clientID]
	s.mu.Unlock()
	if known {
		for _, u := range uris {
			if u == redirectURI {
				return true
			}
		}
		return false
	}
	u, err := url.Parse(redirectURI)
	return err == nil && trustedRedirectHosts[u.Hostname()]
}

func (s *Server) authorizeForm(w http.ResponseWriter, r *http.Request) {
	p, err := s.checkAuthorizeParams(r.URL.Query())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = authorizeTmpl.Execute(w, p)
}

func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	p, err := s.checkAuthorizeParams(r.PostForm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.PostForm.Get("token")), []byte(s.Token)) != 1 {
		p.Error = "Invalid token."
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_ = authorizeTmpl.Execute(w, p)
		return
	}
	code := randToken()
	s.mu.Lock()
	s.pruneCodesLocked()
	s.codes[code] = authCode{
		clientID:    p.ClientID,
		redirectURI: p.RedirectURI,
		challenge:   p.Challenge,
		expires:     time.Now().Add(codeTTL),
	}
	s.mu.Unlock()
	dest, _ := url.Parse(p.RedirectURI)
	q := dest.Query()
	q.Set("code", code)
	if p.State != "" {
		q.Set("state", p.State)
	}
	dest.RawQuery = q.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "bad form")
		return
	}
	if gt := r.PostForm.Get("grant_type"); gt != "authorization_code" {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", gt)
		return
	}
	code := r.PostForm.Get("code")
	s.mu.Lock()
	rec, ok := s.codes[code]
	delete(s.codes, code) // single use
	s.mu.Unlock()
	if !ok || time.Now().After(rec.expires) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired code")
		return
	}
	if cid := r.PostForm.Get("client_id"); cid != "" && cid != rec.clientID {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "client_id mismatch")
		return
	}
	if ru := r.PostForm.Get("redirect_uri"); ru != "" && ru != rec.redirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != rec.challenge {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": s.Token,
		"token_type":   "Bearer",
	})
}

func (s *Server) pruneCodesLocked() {
	now := time.Now()
	for c, rec := range s.codes {
		if now.After(rec.expires) {
			delete(s.codes, c)
		}
	}
}

func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	return u.Scheme == "http" && (host == "localhost" || host == "127.0.0.1")
}

func randToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}
