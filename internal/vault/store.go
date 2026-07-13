package vault

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/justengland/horde/internal/git"
	"github.com/justengland/horde/internal/paths"
)

const (
	DefaultReadLimit   = 100 * 1024
	DefaultSearchLimit = 20
	MaxSearchLimit     = 100
)

type Store struct {
	Paths              *paths.Vault
	MaxNoteBytes       int64
	MaxAttachmentBytes int64
	AttachmentMIME     map[string]string // ext (no dot) → MIME
	ReadLimitBytes     int64
	Git                *git.Repo
	ReadOnly           bool
}

type WikiInfo struct {
	Name        string    `json:"name"`
	NoteCount   int       `json:"note_count"`
	HasTaxonomy bool      `json:"has_taxonomy"`
	Modified    time.Time `json:"modified"`
}

type NoteMeta struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type NoteRead struct {
	Path               string         `json:"path"`
	Frontmatter        map[string]any `json:"frontmatter,omitempty"`
	Body               string         `json:"body"`
	Size               int64          `json:"size"`
	Modified           time.Time      `json:"modified"`
	Offset             int64          `json:"offset,omitempty"`
	Truncated          bool           `json:"truncated,omitempty"`
	HasConflictMarkers bool           `json:"has_conflict_markers,omitempty"`
	IncludeFrontmatter bool           `json:"-"`
}

type SearchHit struct {
	Path       string    `json:"path"`
	Line       int       `json:"line"`
	Snippet    string    `json:"snippet"`
	MatchCount int       `json:"match_count"`
	Score      int       `json:"score"`
	Modified   time.Time `json:"-"`
}

func (s *Store) ListWikis() ([]WikiInfo, error) {
	entries, err := os.ReadDir(s.Paths.Root)
	if err != nil {
		return nil, err
	}
	var out []WikiInfo
	for _, ent := range entries {
		if !ent.IsDir() || s.Paths.IsExcludedName(ent.Name()) {
			continue
		}
		wikiPath := filepath.Join(s.Paths.Root, ent.Name())
		count, latest, err := s.countNotes(wikiPath)
		if err != nil {
			return nil, err
		}
		taxPath := filepath.Join(wikiPath, "taxonomy.md")
		hasTax := false
		if st, err := os.Stat(taxPath); err == nil && !st.IsDir() {
			hasTax = true
			if st.ModTime().After(latest) {
				latest = st.ModTime()
			}
		}
		out = append(out, WikiInfo{
			Name:        ent.Name(),
			NoteCount:   count,
			HasTaxonomy: hasTax,
			Modified:    latest,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) countNotes(dir string) (int, time.Time, error) {
	var count int
	var latest time.Time
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != dir && s.Paths.IsExcludedName(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !paths.AllowedReadExt(d.Name()) || !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		count++
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return count, latest, err
}

func (s *Store) ListNotes(prefix string, recursive bool, sortBy string, limit int) ([]NoteMeta, error) {
	if limit <= 0 {
		limit = 200
	}
	base, err := s.Paths.ResolveDir(prefix)
	if err != nil {
		return nil, err
	}
	var notes []NoteMeta
	walkFn := func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != base && s.Paths.IsExcludedName(d.Name()) {
				return filepath.SkipDir
			}
			if !recursive && path != base {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(d.Name()), ".md") {
			return nil
		}
		rel, err := s.Paths.ToRelative(path)
		if err != nil {
			return err
		}
		if !s.Paths.IsNotePath(rel) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		notes = append(notes, NoteMeta{
			Path:     rel,
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
		if len(notes) >= limit {
			return fs.SkipAll
		}
		return nil
	}
	if err := filepath.WalkDir(base, walkFn); err != nil && err != fs.SkipAll {
		return nil, err
	}
	sortNotes(notes, sortBy)
	if len(notes) > limit {
		notes = notes[:limit]
	}
	return notes, nil
}

func sortNotes(notes []NoteMeta, sortBy string) {
	switch sortBy {
	case "name":
		sort.Slice(notes, func(i, j int) bool { return notes[i].Path < notes[j].Path })
	default:
		sort.Slice(notes, func(i, j int) bool {
			if notes[i].Modified.Equal(notes[j].Modified) {
				return notes[i].Path < notes[j].Path
			}
			return notes[i].Modified.After(notes[j].Modified)
		})
	}
}

func (s *Store) ReadNote(apiPath string, includeFrontmatter bool, offset, length int64) (*NoteRead, error) {
	abs, err := s.Paths.Resolve(apiPath)
	if err != nil {
		return nil, err
	}
	if !paths.AllowedReadExt(filepath.Base(apiPath)) {
		return nil, fmt.Errorf("unsupported file extension")
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	content := string(data)
	hasConflict := HasConflictMarkers(content)
	var frontmatter map[string]any
	if includeFrontmatter {
		parsed := ParseFrontmatter(data)
		frontmatter = parsed.Frontmatter
		content = parsed.Body
	}
	if offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0")
	}
	if offset > int64(len(content)) {
		return nil, fmt.Errorf("offset %d exceeds content length %d", offset, len(content))
	}
	chunk := content[offset:]
	limit := s.ReadLimitBytes
	if length > 0 {
		limit = length
	}
	truncated := int64(len(chunk)) > limit
	if truncated {
		chunk = chunk[:limit]
	}
	out := &NoteRead{
		Path:               apiPath,
		Size:               info.Size(),
		Modified:           info.ModTime(),
		Offset:             offset,
		Truncated:          truncated,
		HasConflictMarkers: hasConflict,
		IncludeFrontmatter: includeFrontmatter,
		Body:               chunk,
	}
	if includeFrontmatter {
		out.Frontmatter = frontmatter
	}
	return out, nil
}

// NoteHistory returns git commits for a note path.
func (s *Store) NoteHistory(apiPath string, limit int) ([]git.HistoryEntry, error) {
	if s.Git == nil {
		return nil, fmt.Errorf("git is disabled")
	}
	if err := paths.ValidateAPIPath(apiPath); err != nil {
		return nil, err
	}
	if _, err := s.Paths.Resolve(apiPath); err != nil {
		return nil, err
	}
	return s.Git.FileHistory(apiPath, limit)
}

// RestoreNote checks out a note at ref and commits it.
func (s *Store) RestoreNote(apiPath, ref string) (*WriteResult, error) {
	if err := s.checkWritable(); err != nil {
		return nil, err
	}
	if err := s.validateWritePath(apiPath); err != nil {
		return nil, err
	}
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("ref is required")
	}
	if s.Git == nil {
		return nil, fmt.Errorf("git is disabled")
	}
	abs, err := s.Paths.Resolve(apiPath)
	if err != nil {
		return nil, err
	}
	hash, err := s.Git.RestoreFile("restore", apiPath, ref)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	if HasConflictMarkers(string(data)) {
		return nil, fmt.Errorf("restored content still has conflict markers; pick another ref")
	}
	return &WriteResult{
		Path:         apiPath,
		Commit:       hash,
		BytesWritten: int64(len(data)),
	}, nil
}

type SearchParams struct {
	Query         string
	PathPrefix    string
	Limit         int
	CaseSensitive bool
	Regex         bool
}

func (s *Store) SearchNotes(p SearchParams) ([]SearchHit, error) {
	if strings.TrimSpace(p.Query) == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		limit = MaxSearchLimit
	}
	terms, re, err := compileSearchTerms(p)
	if err != nil {
		return nil, err
	}
	base, err := s.Paths.ResolveDir(p.PathPrefix)
	if err != nil {
		return nil, err
	}

	type job struct{ path string }
	jobs := make(chan job, 64)
	results := make(chan SearchHit, limit)
	var wg sync.WaitGroup
	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				hit, ok, err := s.searchFile(j.path, terms, re, p.CaseSensitive)
				if err != nil || !ok {
					continue
				}
				if info, err := os.Stat(j.path); err == nil {
					hit.Modified = info.ModTime()
				}
				results <- hit
			}
		}()
	}
	go func() {
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
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
			info, err := d.Info()
			if err != nil {
				return err
			}
			if info.Size() > s.MaxNoteBytes {
				return nil
			}
			jobs <- job{path: path}
			return nil
		})
		close(jobs)
		wg.Wait()
		close(results)
	}()
	var hits []SearchHit
	for h := range results {
		hits = append(hits, h)
		if len(hits) >= limit*4 {
			break
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if !hits[i].Modified.Equal(hits[j].Modified) {
			return hits[i].Modified.After(hits[j].Modified)
		}
		return hits[i].Path < hits[j].Path
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func compileSearchTerms(p SearchParams) ([]string, *regexp.Regexp, error) {
	if p.Regex {
		pattern := p.Query
		if !p.CaseSensitive && !strings.HasPrefix(pattern, "(?i)") {
			pattern = "(?i)" + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid regex: %w", err)
		}
		return nil, re, nil
	}
	query := p.Query
	if !p.CaseSensitive {
		query = strings.ToLower(query)
	}
	var terms []string
	for _, t := range strings.Fields(query) {
		if t != "" {
			terms = append(terms, t)
		}
	}
	if len(terms) == 0 {
		return nil, nil, fmt.Errorf("query is required")
	}
	return terms, nil, nil
}

func (s *Store) searchFile(path string, terms []string, re *regexp.Regexp, caseSensitive bool) (SearchHit, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SearchHit{}, false, err
	}
	if looksBinary(data) {
		return SearchHit{}, false, nil
	}
	rel, err := s.Paths.ToRelative(path)
	if err != nil {
		return SearchHit{}, false, err
	}
	base := filepath.Base(path)
	content := string(data)
	scan := content
	if !caseSensitive {
		scan = strings.ToLower(content)
	}
	matchCount := 0
	if re != nil {
		locs := re.FindAllStringIndex(content, -1)
		matchCount = len(locs)
	} else {
		for _, term := range terms {
			matchCount += strings.Count(scan, term)
		}
	}
	if matchCount == 0 {
		return SearchHit{}, false, nil
	}
	if re == nil {
		for _, term := range terms {
			if !strings.Contains(scan, term) {
				return SearchHit{}, false, nil
			}
		}
	}
	score := scoreHit(base, content, terms, re, caseSensitive)
	line, snippet := firstSnippet(content, terms, re, caseSensitive)
	return SearchHit{
		Path:       rel,
		Line:       line,
		Snippet:    snippet,
		MatchCount: matchCount,
		Score:      score,
	}, true, nil
}

func looksBinary(data []byte) bool {
	n := min(len(data), 512)
	return strings.Contains(string(data[:n]), "\x00")
}

func scoreHit(base, content string, terms []string, re *regexp.Regexp, caseSensitive bool) int {
	score := 0
	lowerBase := strings.ToLower(base)
	lowerContent := content
	if !caseSensitive {
		lowerContent = strings.ToLower(content)
	}
	if re != nil {
		if re.MatchString(base) {
			score += 100
		}
		if re.MatchString(content) {
			score += 10
		}
		return score
	}
	for _, term := range terms {
		if strings.Contains(lowerBase, term) {
			score += 100
		}
		if strings.Contains(lowerContent, "# "+term) || strings.Contains(lowerContent, "## "+term) {
			score += 50
		}
		if strings.HasPrefix(lowerContent, "---") && strings.Contains(lowerContent, term) {
			score += 30
		}
		if strings.Contains(lowerContent, term) {
			score += 10
		}
	}
	return score
}

func firstSnippet(content string, terms []string, re *regexp.Regexp, caseSensitive bool) (int, string) {
	lines := strings.Split(content, "\n")
	for lineNo, line := range lines {
		check := line
		if !caseSensitive {
			check = strings.ToLower(line)
		}
		matched := false
		if re != nil {
			matched = re.MatchString(line)
		} else {
			for _, term := range terms {
				if strings.Contains(check, term) {
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		start := max(0, lineNo-1)
		end := min(len(lines), lineNo+2)
		snippet := strings.Join(lines[start:end], "\n")
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		if !utf8.ValidString(snippet) {
			snippet = strings.ToValidUTF8(snippet, "")
		}
		return lineNo + 1, snippet
	}
	return 0, ""
}
