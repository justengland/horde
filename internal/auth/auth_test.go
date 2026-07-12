package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justengland/horde/internal/auth"
)

func TestBearerMiddleware(t *testing.T) {
	const token = "secret-token"
	called := false
	handler := auth.BearerMiddleware(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("missing", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code %d", rec.Code)
		}
		if called {
			t.Fatal("next handler should not run")
		}
	})

	t.Run("wrong", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer wrong")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code %d", rec.Code)
		}
	})

	t.Run("valid", func(t *testing.T) {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code %d", rec.Code)
		}
		if !called {
			t.Fatal("next handler should run")
		}
	})
}
