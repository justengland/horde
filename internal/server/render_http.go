package server

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/justengland/horde/internal/render"
	"github.com/justengland/horde/internal/vault"
)

// handleRender serves the one public-but-signed attachment route (ADR-10).
// It serves precisely what read_attachment serves — same allowlist, size
// cap, and path safety — and nothing is ever served unsigned.
func handleRender(store *vault.Store, signer *render.Signer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		apiPath := r.PathValue("path")
		// no-store on every response: horde sits behind Cloudflare, and an
		// edge-cached body would outlive the link's expiry.
		w.Header().Set("Cache-Control", "private, no-store")
		q := r.URL.Query()
		if err := signer.Verify(apiPath, q.Get("exp"), q.Get("sig")); err != nil {
			status := renderStatus(err)
			logRender(apiPath, start, status, 0)
			http.Error(w, "invalid or expired render link", status)
			return
		}
		att, err := store.ReadAttachment(apiPath)
		if err != nil {
			logRender(apiPath, start, http.StatusNotFound, 0)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", att.MIMEType)
		w.Header().Set("Content-Length", strconv.FormatInt(att.Size, 10))
		logRender(apiPath, start, http.StatusOK, att.Size)
		_, _ = w.Write(att.Bytes)
	}
}

func renderStatus(err error) int {
	if errors.Is(err, render.ErrExpired) {
		return http.StatusGone
	}
	return http.StatusForbidden
}

func logRender(apiPath string, start time.Time, status int, bytes int64) {
	attrs := []any{
		"route", "render",
		"path", apiPath,
		"status", status,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if bytes > 0 {
		attrs = append(attrs, "bytes", bytes)
	}
	if status == http.StatusOK {
		slog.Info("render", attrs...)
	} else {
		slog.Warn("render", attrs...)
	}
}
