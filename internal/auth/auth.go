package auth

import (
	"crypto/subtle"
	"net/http"
)

func BearerMiddleware(token string, next http.Handler) http.Handler {
	return BearerMiddlewareWithMetadata(token, "", next)
}

// BearerMiddlewareWithMetadata additionally advertises the OAuth protected
// resource metadata URL (RFC 9728) on 401s so MCP clients can discover the
// authorization flow.
func BearerMiddlewareWithMetadata(token, resourceMetadataURL string, next http.Handler) http.Handler {
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			if resourceMetadataURL != "" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+resourceMetadataURL+`"`)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
