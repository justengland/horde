package server

import (
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/justengland/horde/internal/render"
	"github.com/justengland/horde/internal/vault"
)

// handleRender serves the one public-but-signed attachment route (ADR-10).
// It applies the same allowlist and path safety as read_attachment, and
// nothing is ever served unsigned.
//
// It streams via http.ServeContent rather than buffering, which also answers
// Range requests — what a browser's <video> element needs to seek, and what a
// paused download needs to resume. ?download=1 flips Content-Disposition from
// inline to attachment so a click saves the file instead of rendering it.
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
		att, err := store.OpenAttachment(apiPath)
		if err != nil {
			logRender(apiPath, start, http.StatusNotFound, 0)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		defer att.File.Close()

		disposition := "inline"
		if download, _ := strconv.ParseBool(q.Get("download")); download {
			disposition = "attachment"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition,
			map[string]string{"filename": att.Name}))
		w.Header().Set("Content-Type", att.MIMEType)
		// ServeContent negotiates Range and sets Content-Length itself; a
		// zero modtime keeps it from emitting Last-Modified validators that
		// would invite caching we just told the edge not to do.
		logRender(apiPath, start, http.StatusOK, att.Size)
		http.ServeContent(w, r, att.Name, time.Time{}, att.File)
	}
}

// downloadURL turns a minted Render Link into one that saves to disk.
// The signature covers path and expiry only, so the extra parameter grants
// no access a plain Render Link doesn't already grant.
func downloadURL(minted string) string {
	if minted == "" {
		return ""
	}
	return fmt.Sprintf("%s&download=1", minted)
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
