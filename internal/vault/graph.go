package vault

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var wikilinkRe = regexp.MustCompile(`!?\[\[([^\]]+)\]\]`)

type GraphPage struct {
	Path        string   `json:"path"`
	OutLinks    []string `json:"out_links"`
	InLinkCount int      `json:"in_link_count"`
}

type BrokenLink struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

type LinkGraph struct {
	Pages       []GraphPage  `json:"pages"`
	Orphans     []string     `json:"orphans"`
	BrokenLinks []BrokenLink `json:"broken_links"`
}

// LinkGraph parses [[wikilinks]] across one wiki and returns the adjacency map.
func (s *Store) LinkGraph(wiki string) (*LinkGraph, error) {
	wiki = strings.Trim(wiki, "/")
	if wiki == "" || strings.Contains(wiki, "/") {
		return nil, fmt.Errorf("wiki must be a single top-level folder name")
	}
	base, err := s.Paths.Resolve(wiki)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(base)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("wiki is not a directory")
	}

	var pages []string
	bodies := make(map[string]string)
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != base && s.Paths.IsExcludedName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		rel, err := s.Paths.ToRelative(path)
		if err != nil || !s.Paths.IsNotePath(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		pages = append(pages, rel)
		bodies[rel] = string(data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(pages)

	// Indexes for resolution within this wiki.
	byRel := make(map[string]string, len(pages))     // vault-relative path → path
	byWikiRel := make(map[string]string, len(pages)) // path relative to wiki → path
	byBase := make(map[string][]string, len(pages))  // basename without .md → paths
	for _, p := range pages {
		byRel[p] = p
		byRel[strings.TrimSuffix(p, ".md")] = p
		wikiRel := strings.TrimPrefix(p, wiki+"/")
		byWikiRel[wikiRel] = p
		byWikiRel[strings.TrimSuffix(wikiRel, ".md")] = p
		baseName := strings.TrimSuffix(filepath.Base(p), ".md")
		byBase[strings.ToLower(baseName)] = append(byBase[strings.ToLower(baseName)], p)
	}

	resolve := func(target string) (string, bool) {
		target = normalizeWikilinkTarget(target)
		if target == "" {
			return "", false
		}
		if p, ok := byRel[target]; ok {
			return p, true
		}
		if p, ok := byRel[wiki+"/"+target]; ok {
			return p, true
		}
		if p, ok := byWikiRel[target]; ok {
			return p, true
		}
		cands := byBase[strings.ToLower(filepath.Base(strings.TrimSuffix(target, ".md")))]
		if len(cands) == 1 {
			return cands[0], true
		}
		return "", false
	}

	type pageInfo struct {
		path string
		out  []string
		in   int
	}
	infos := make(map[string]*pageInfo, len(pages))
	for _, p := range pages {
		infos[p] = &pageInfo{path: p}
	}
	var broken []BrokenLink

	for _, p := range pages {
		rawTargets := extractWikilinkTargets(bodies[p])
		seenOut := make(map[string]struct{})
		var out []string
		for _, raw := range rawTargets {
			norm := normalizeWikilinkTarget(raw)
			if norm == "" {
				continue
			}
			if _, dup := seenOut[norm]; dup {
				continue
			}
			seenOut[norm] = struct{}{}
			out = append(out, norm)
			if resolved, ok := resolve(norm); ok {
				infos[resolved].in++
			} else if !isAttachmentTarget(norm, s.AttachmentMIME) {
				broken = append(broken, BrokenLink{Path: p, Target: norm})
			}
		}
		infos[p].out = out
	}

	var graphPages []GraphPage
	var orphans []string
	for _, p := range pages {
		info := infos[p]
		graphPages = append(graphPages, GraphPage{
			Path:        info.path,
			OutLinks:    info.out,
			InLinkCount: info.in,
		})
		if info.in == 0 {
			orphans = append(orphans, p)
		}
	}
	return &LinkGraph{
		Pages:       graphPages,
		Orphans:     orphans,
		BrokenLinks: broken,
	}, nil
}

func extractWikilinkTargets(body string) []string {
	matches := wikilinkRe.FindAllStringSubmatch(body, -1)
	var out []string
	for _, m := range matches {
		if len(m) > 1 {
			out = append(out, m[1])
		}
	}
	return out
}

func normalizeWikilinkTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.Index(raw, "|"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "#"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "\\", "/")
	return strings.Trim(raw, "/")
}

func isAttachmentTarget(target string, mime map[string]string) bool {
	if mime == nil {
		return false
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(target), "."))
	_, ok := mime[ext]
	return ok
}
