package auth

import (
	"crypto/subtle"
	"net/http"
)

func BearerMiddleware(token string, next http.Handler) http.Handler {
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
