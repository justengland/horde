package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/justengland/horde/internal/auth"
	"github.com/justengland/horde/internal/git"
	"github.com/justengland/horde/internal/paths"
	"github.com/justengland/horde/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.2.0-m2"

const Instructions = "Before creating or editing notes in a wiki, read `<wiki>/taxonomy.md` and follow it. If it does not exist, propose one using `create_note` before making other changes. Paths are root-relative with forward slashes and must begin with a wiki name (e.g. `network/opnsense/wan-failover.md`). Prefer `update_note` patch mode over replace. Deletes are git-recoverable. `move_note` does not rewrite wikilinks."

type Deps struct {
	Store *vault.Store
	Token string
}

func NewHTTPServer(deps Deps) http.Handler {
	mcpServer := newMCPServer(deps.Store)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpServer
	}, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeHealth(w, deps.Store.Paths.Root)
	})
	mux.Handle("/mcp", auth.BearerMiddleware(deps.Token, mcpHandler))

	return mux
}

func writeHealth(w http.ResponseWriter, root string) {
	vaultOK := false
	if st, err := os.Stat(root); err == nil && st.IsDir() {
		if _, err := os.ReadDir(root); err == nil {
			vaultOK = true
		}
	}
	resp := map[string]any{
		"status":   "ok",
		"version":  Version,
		"vault":    root,
		"vault_ok": vaultOK,
		"git_ok":   gitOK(root),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func gitOK(root string) bool {
	cmd := exec.Command("git", "-C", root, "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

func CheckGitRepo(root string) error {
	if gitOK(root) {
		return nil
	}
	return fmt.Errorf("%s is not a git repository (set HORDE_GIT=off to disable)", root)
}

func newMCPServer(store *vault.Store) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "horde",
		Version: Version,
	}, &mcp.ServerOptions{
		Instructions: Instructions,
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_wikis",
		Description: "Orientation call — enumerate top-level wikis under the vault root. Example: list_wikis() → [{name, note_count, has_taxonomy, modified}]. Paths in other tools are root-relative and must start with a wiki name.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listWikisOut, error) {
		wikis, err := store.ListWikis()
		if err != nil {
			return nil, listWikisOut{}, err
		}
		return nil, listWikisOut{Wikis: wikis}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_notes",
		Description: "List markdown notes under a path prefix. Example: list_notes({path_prefix: \"network\", recursive: true, sort: \"modified\", limit: 50}). Returns [{path, size, modified}] with root-relative paths.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listNotesIn) (*mcp.CallToolResult, listNotesOut, error) {
		recursive := true
		if in.Recursive != nil {
			recursive = *in.Recursive
		}
		notes, err := store.ListNotes(in.PathPrefix, recursive, in.Sort, in.Limit)
		if err != nil {
			return nil, listNotesOut{}, err
		}
		return nil, listNotesOut{Notes: notes}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_note",
		Description: "Read a single markdown note. Example: read_note({path: \"network/opnsense/wan-failover.md\", include_frontmatter: true}). Large notes may be truncated at 100 KiB.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in readNoteIn) (*mcp.CallToolResult, readNoteOut, error) {
		include := true
		if in.IncludeFrontmatter != nil {
			include = *in.IncludeFrontmatter
		}
		note, err := store.ReadNote(in.Path, include)
		if err != nil {
			return nil, readNoteOut{}, err
		}
		out := readNoteOut{
			Path:      note.Path,
			Body:      note.Body,
			Size:      note.Size,
			Modified:  note.Modified,
			Truncated: note.Truncated,
		}
		if include && note.Frontmatter != nil {
			out.Frontmatter = note.Frontmatter
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_notes",
		Description: "Full-text search across markdown notes (stateless scan). Example: search_notes({query: \"opnsense deco\", path_prefix: \"network\", limit: 20}). Space-separated terms are ANDed. Returns [{path, line, snippet, match_count, score}].",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in searchNotesIn) (*mcp.CallToolResult, searchNotesOut, error) {
		hits, err := store.SearchNotes(vault.SearchParams{
			Query:         in.Query,
			PathPrefix:    in.PathPrefix,
			Limit:         in.Limit,
			CaseSensitive: in.CaseSensitive,
			Regex:         in.Regex,
		})
		if err != nil {
			return nil, searchNotesOut{}, err
		}
		return nil, searchNotesOut{Results: hits}, nil
	})

	if !store.ReadOnly {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "create_note",
			Description: "Create a markdown note inside a wiki. Example: create_note({path: \"network/opnsense/new-topic.md\", content: \"# Title\\n\\nBody\", overwrite: false}). Fails if the file exists unless overwrite is true.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in createNoteIn) (*mcp.CallToolResult, vault.WriteResult, error) {
			start := time.Now()
			res, err := store.CreateNote(in.Path, in.Content, in.Overwrite)
			var commit string
			if res != nil {
				commit = res.Commit
			}
			logTool("create_note", []string{in.Path}, start, err, commit, resBytes(res))
			if err != nil {
				return nil, vault.WriteResult{}, err
			}
			return nil, *res, nil
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "update_note",
			Description: "Update a note. Prefer patch mode. Example: update_note({path: \"network/opnsense/wan-failover.md\", mode: \"patch\", old_str: \"## Old\", new_str: \"## New\\n\\nDetails\"}). Modes: patch, append, prepend, replace.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in updateNoteIn) (*mcp.CallToolResult, updateNoteOut, error) {
			start := time.Now()
			res, err := store.UpdateNote(in.Path, in.Mode, in.OldStr, in.NewStr, in.Content)
			var commit string
			var bytes int64
			if res != nil {
				commit = res.Commit
				bytes = res.BytesWritten
			}
			logTool("update_note", []string{in.Path}, start, err, commit, bytes)
			if err != nil {
				return nil, updateNoteOut{}, err
			}
			return nil, updateNoteOut{Path: res.Path, Mode: in.Mode, Commit: res.Commit, BytesWritten: res.BytesWritten}, nil
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "delete_note",
			Description: "Soft-delete a note (git-recoverable). Example: delete_note({path: \"network/opnsense/old.md\", confirm: true}). confirm must be true.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in deleteNoteIn) (*mcp.CallToolResult, vault.DeleteResult, error) {
			if !in.Confirm {
				return nil, vault.DeleteResult{}, fmt.Errorf("confirm must be true")
			}
			start := time.Now()
			res, err := store.DeleteNote(in.Path)
			var commit string
			if res != nil {
				commit = res.Commit
			}
			logTool("delete_note", []string{in.Path}, start, err, commit, 0)
			if err != nil {
				return nil, vault.DeleteResult{}, err
			}
			return nil, *res, nil
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "move_note",
			Description: "Rename or move a note. Does not rewrite wikilinks. Example: move_note({from: \"network/old.md\", to: \"network/archive/old.md\", overwrite: false}).",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in moveNoteIn) (*mcp.CallToolResult, vault.MoveResult, error) {
			start := time.Now()
			res, err := store.MoveNote(in.From, in.To, in.Overwrite)
			var commit string
			if res != nil {
				commit = res.Commit
			}
			logTool("move_note", []string{in.From, in.To}, start, err, commit, 0)
			if err != nil {
				return nil, vault.MoveResult{}, err
			}
			return nil, *res, nil
		})
	}

	return s
}

func resBytes(res *vault.WriteResult) int64 {
	if res == nil {
		return 0
	}
	return res.BytesWritten
}

func logTool(tool string, paths []string, start time.Time, err error, commit string, bytes int64) {
	attrs := []any{
		"tool", tool,
		"paths", paths,
		"duration_ms", time.Since(start).Milliseconds(),
	}
	if err != nil {
		slog.Warn("tool call", append(attrs, "result", "error", "err", err.Error())...)
		return
	}
	if commit != "" {
		attrs = append(attrs, "commit", commit)
	}
	if bytes > 0 {
		attrs = append(attrs, "bytes", bytes)
	}
	slog.Info("tool call", append(attrs, "result", "ok")...)
}

type listWikisOut struct {
	Wikis []vault.WikiInfo `json:"wikis"`
}

type listNotesIn struct {
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"Root-relative prefix; empty means vault root"`
	Recursive  *bool  `json:"recursive,omitempty" jsonschema:"Whether to recurse into subdirectories"`
	Sort       string `json:"sort,omitempty" jsonschema:"Sort by name or modified"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum number of notes to return"`
}

type listNotesOut struct {
	Notes []vault.NoteMeta `json:"notes"`
}

type readNoteIn struct {
	Path               string `json:"path" jsonschema:"Root-relative path to the note"`
	IncludeFrontmatter *bool  `json:"include_frontmatter,omitempty" jsonschema:"Parse YAML frontmatter when present"`
}

type readNoteOut struct {
	Path        string         `json:"path"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Body        string         `json:"body"`
	Size        int64          `json:"size"`
	Modified    time.Time      `json:"modified"`
	Truncated   bool           `json:"truncated,omitempty"`
}

type searchNotesIn struct {
	Query         string `json:"query" jsonschema:"Search query; space-separated terms are ANDed"`
	PathPrefix    string `json:"path_prefix,omitempty" jsonschema:"Limit search to this root-relative prefix"`
	Limit         int    `json:"limit,omitempty" jsonschema:"Maximum results (default 20, max 100)"`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema:"Case-sensitive matching"`
	Regex         bool   `json:"regex,omitempty" jsonschema:"Treat query as a regular expression"`
}

type searchNotesOut struct {
	Results []vault.SearchHit `json:"results"`
}

type createNoteIn struct {
	Path      string `json:"path" jsonschema:"Root-relative path ending in .md"`
	Content   string `json:"content" jsonschema:"Full note content"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"Overwrite if the file already exists"`
}

type updateNoteIn struct {
	Path    string `json:"path" jsonschema:"Root-relative path to the note"`
	Mode    string `json:"mode" jsonschema:"patch, append, prepend, or replace"`
	OldStr  string `json:"old_str,omitempty" jsonschema:"String to replace (patch mode; must be unique)"`
	NewStr  string `json:"new_str,omitempty" jsonschema:"Replacement string (patch mode)"`
	Content string `json:"content,omitempty" jsonschema:"Content for append, prepend, or replace modes"`
}

type updateNoteOut struct {
	Path         string `json:"path"`
	Mode         string `json:"mode"`
	Commit       string `json:"commit,omitempty"`
	BytesWritten int64  `json:"bytes_written"`
}

type deleteNoteIn struct {
	Path    string `json:"path" jsonschema:"Root-relative path to delete"`
	Confirm bool   `json:"confirm" jsonschema:"Must be true to confirm deletion"`
}

type moveNoteIn struct {
	From      string `json:"from" jsonschema:"Source path"`
	To        string `json:"to" jsonschema:"Destination path"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"Overwrite destination if it exists"`
}

func NewStore(root string, ignore []string, maxNoteBytes int64, gitRepo *git.Repo, readOnly bool) (*vault.Store, error) {
	pv, err := paths.NewVault(root, ignore)
	if err != nil {
		return nil, err
	}
	return &vault.Store{
		Paths:          pv,
		MaxNoteBytes:   maxNoteBytes,
		ReadLimitBytes: vault.DefaultReadLimit,
		Git:            gitRepo,
		ReadOnly:       readOnly,
	}, nil
}

func EnsureVaultRoot(root string) error {
	st, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("vault root: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("vault root is not a directory")
	}
	return nil
}
