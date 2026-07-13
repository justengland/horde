package paths

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const MaxSegmentLen = 255

var DefaultExcluded = []string{".obsidian", ".git", ".trash", ".claude"}

// Vault resolves root-relative vault paths safely.
type Vault struct {
	Root     string
	Excluded map[string]struct{}
}

func NewVault(root string, extraIgnore []string) (*Vault, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve vault root: %w", err)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("abs vault root: %w", err)
	}
	excluded := make(map[string]struct{}, len(DefaultExcluded)+len(extraIgnore))
	for _, name := range DefaultExcluded {
		excluded[name] = struct{}{}
	}
	for _, name := range extraIgnore {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		excluded[name] = struct{}{}
	}
	return &Vault{Root: rootAbs, Excluded: excluded}, nil
}

// ToRelative converts an absolute path under Root to a forward-slash relative path.
func (v *Vault) ToRelative(abs string) (string, error) {
	rel, err := filepath.Rel(v.Root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes vault root")
	}
	return filepath.ToSlash(rel), nil
}

// Resolve maps a root-relative API path to an absolute filesystem path.
func (v *Vault) Resolve(apiPath string) (string, error) {
	if err := ValidateAPIPath(apiPath); err != nil {
		return "", err
	}
	clean := filepath.FromSlash(strings.TrimPrefix(filepath.Clean("/"+apiPath), "/"))
	if clean == "." {
		return "", fmt.Errorf("empty path")
	}
	if err := checkSegments(clean); err != nil {
		return "", err
	}
	if err := v.checkExcludedComponents(clean); err != nil {
		return "", err
	}
	abs := filepath.Join(v.Root, clean)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return abs, nil
		}
		return "", fmt.Errorf("resolve path: %w", err)
	}
	rootResolved, err := filepath.EvalSymlinks(v.Root)
	if err != nil {
		return "", fmt.Errorf("resolve vault root: %w", err)
	}
	rel, err := filepath.Rel(rootResolved, resolved)
	if err != nil {
		return "", fmt.Errorf("path escapes vault root")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes vault root")
	}
	return resolved, nil
}

// ResolveDir resolves a prefix for listing; empty prefix means vault root.
func (v *Vault) ResolveDir(prefix string) (string, error) {
	if prefix == "" {
		return v.Root, nil
	}
	return v.Resolve(prefix)
}

// IsExcludedName reports whether a single path component is excluded.
func (v *Vault) IsExcludedName(name string) bool {
	_, ok := v.Excluded[name]
	return ok
}

// IsNotePath reports whether rel is a note inside a wiki (wiki/...).
func (v *Vault) IsNotePath(rel string) bool {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return false
	}
	return !v.IsExcludedName(parts[0])
}

func ValidateAPIPath(apiPath string) error {
	if apiPath == "" {
		return fmt.Errorf("empty path")
	}
	if !utf8.ValidString(apiPath) {
		return fmt.Errorf("path is not valid UTF-8")
	}
	if filepath.IsAbs(apiPath) || strings.HasPrefix(apiPath, "/") {
		return fmt.Errorf("absolute paths are not allowed")
	}
	if strings.Contains(apiPath, "..") {
		return fmt.Errorf("parent segments are not allowed")
	}
	return nil
}

func checkSegments(clean string) error {
	for _, seg := range strings.Split(clean, string(os.PathSeparator)) {
		if seg == "" {
			continue
		}
		if len(seg) > MaxSegmentLen {
			return fmt.Errorf("path segment too long")
		}
	}
	return nil
}

func (v *Vault) checkExcludedComponents(clean string) error {
	for _, seg := range strings.Split(clean, string(os.PathSeparator)) {
		if v.IsExcludedName(seg) {
			return fmt.Errorf("path is in an excluded directory")
		}
	}
	return nil
}

// AllowedReadExt reports whether extension may be read as a note.
func AllowedReadExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".md" || ext == ".canvas"
}

// AllowedWriteExt reports whether extension may be written (M2).
func AllowedWriteExt(name string) bool {
	return strings.ToLower(filepath.Ext(name)) == ".md"
}

// WikiURI builds a wiki:// URI from a root-relative vault path.
// Example: network/diagrams/wan.png → wiki://network/diagrams/wan.png
func WikiURI(apiPath string) string {
	apiPath = strings.Trim(filepath.ToSlash(apiPath), "/")
	return "wiki://" + apiPath
}

// ParseWikiURI accepts a wiki:// URI or a plain root-relative path and returns
// the root-relative API path.
func ParseWikiURI(pathOrURI string) (string, error) {
	pathOrURI = strings.TrimSpace(pathOrURI)
	if pathOrURI == "" {
		return "", fmt.Errorf("empty path")
	}
	if !strings.Contains(pathOrURI, "://") {
		return filepath.ToSlash(strings.TrimPrefix(pathOrURI, "/")), nil
	}
	u, err := url.Parse(pathOrURI)
	if err != nil {
		return "", fmt.Errorf("invalid URI: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "wiki") {
		return "", fmt.Errorf("unsupported URI scheme %q (want wiki)", u.Scheme)
	}
	var apiPath string
	if u.Host != "" {
		apiPath = u.Host + u.Path
	} else {
		apiPath = u.Path
	}
	apiPath = strings.TrimPrefix(filepath.ToSlash(apiPath), "/")
	if apiPath == "" {
		return "", fmt.Errorf("empty path")
	}
	return apiPath, nil
}
