package vault

import "strings"

// HasConflictMarkers reports whether content contains git merge conflict markers.
func HasConflictMarkers(content string) bool {
	return strings.Contains(content, "<<<<<<<") ||
		strings.Contains(content, ">>>>>>>") ||
		strings.Contains(content, "\n=======\n")
}
