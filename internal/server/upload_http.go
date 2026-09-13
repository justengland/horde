package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/justengland/horde/internal/paths"
	"github.com/justengland/horde/internal/render"
	"github.com/justengland/horde/internal/vault"
)

// handleUpload serves the bearer-gated attachment upload route. Unlike the
// render route it is never signature-gated and never public: a write needs the
// same bearer token /mcp needs. It applies exactly the rules
// create_attachment applies — same allowlist, size cap, and path safety.
func handleUpload(store *vault.Store, signer *render.Signer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		apiPath := r.PathValue("path")
		overwrite, _ := strconv.ParseBool(r.URL.Query().Get("overwrite"))

		// Stream straight to disk: a video upload must not be bounded by
		// how much of it fits in memory. MaxBytesReader is the outer guard;
		// the store enforces the same cap on the bytes it actually writes.
		r.Body = http.MaxBytesReader(w, r.Body, store.MaxStreamBytes+1)
		res, err := store.WriteAttachmentStream(apiPath, r.Body, overwrite)
		if err != nil {
			uploadError(w, apiPath, start, uploadStatus(err), err.Error())
			return
		}
		out := createAttachmentOut{WriteResult: *res, URI: paths.WikiURI(res.Path)}
		if signer != nil {
			out.URL = signer.Mint(res.Path)
			out.DownloadURL = downloadURL(out.URL)
		}
		status := http.StatusOK
		if res.Created {
			status = http.StatusCreated
		}
		slog.Info("upload",
			"route", "upload",
			"path", res.Path,
			"status", status,
			"bytes", res.BytesWritten,
			"commit", res.Commit,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	}
}

func uploadStatus(err error) int {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "read-only"):
		return http.StatusForbidden
	case strings.Contains(msg, "already exists"):
		return http.StatusConflict
	case strings.Contains(msg, "exceeds cap"), strings.Contains(msg, "too large"):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusBadRequest
	}
}

func uploadError(w http.ResponseWriter, apiPath string, start time.Time, status int, msg string) {
	slog.Warn("upload",
		"route", "upload",
		"path", apiPath,
		"status", status,
		"err", msg,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	http.Error(w, msg, status)
}
