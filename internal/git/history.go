package git

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// HistoryEntry is one commit touching a path.
type HistoryEntry struct {
	Hash    string    `json:"hash"`
	Message string    `json:"message"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// FileHistory returns commits that modified relPath, newest first.
func (r *Repo) FileHistory(relPath string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	out, err := r.output(
		"log",
		fmt.Sprintf("-n%d", limit),
		"--format=%H%x00%an%x00%at%x00%s",
		"--follow",
		"--",
		relPath,
	)
	if err != nil {
		return nil, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var entries []HistoryEntry
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\x00")
		if len(parts) != 4 {
			continue
		}
		ts, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse commit date: %w", err)
		}
		entries = append(entries, HistoryEntry{
			Hash:    parts[0],
			Author:  parts[1],
			Date:    time.Unix(ts, 0).UTC(),
			Message: parts[3],
		})
	}
	return entries, nil
}

// RestoreFile checks out relPath at ref and commits the result.
func (r *Repo) RestoreFile(tool, relPath, ref string) (string, error) {
	if err := r.run("checkout", ref, "--", relPath); err != nil {
		return "", fmt.Errorf("git checkout %s -- %s: %w", ref, relPath, err)
	}
	return r.CommitStages(tool, relPath)
}
