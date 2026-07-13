// In-package by design (unlike the repo's black-box test convention): an
// expired link cannot be forged from outside, so the 410 mapping is only
// reachable through the unexported renderStatus.
package server

import (
	"errors"
	"net/http"
	"testing"

	"github.com/justengland/horde/internal/render"
)

func TestRenderStatusMapsExpiryToGone(t *testing.T) {
	if got := renderStatus(render.ErrExpired); got != http.StatusGone {
		t.Fatalf("expired status = %d, want 410", got)
	}
	if got := renderStatus(errors.New("invalid signature")); got != http.StatusForbidden {
		t.Fatalf("invalid status = %d, want 403", got)
	}
}
