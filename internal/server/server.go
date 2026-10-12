package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/justengland/horde/internal/auth"
	"github.com/justengland/horde/internal/config"
	"github.com/justengland/horde/internal/git"
	"github.com/justengland/horde/internal/oauth"
	"github.com/justengland/horde/internal/paths"
	"github.com/justengland/horde/internal/render"
	"github.com/justengland/horde/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.7.0-m7"

const Instructions = "Before creating or editing notes in a wiki, read `<wiki>/taxonomy.md` and follow it. If it does not exist, propose one using `create_note` before making other changes. Paths are root-relative with forward slashes and must begin with a wiki name (e.g. `network/opnsense/wan-failover.md`). Prefer `update_note` patch mode over replace. Deletes are git-recoverable. `move_note` does not rewrite wikilinks. IMPORTANT: To find or view images/PDFs, call `list_attachments` then `read_attachment` (or resources/read on the returned wiki:// URI). `list_notes` / `search_notes` / `read_note` only cover markdown — they never list or return binary files, even when a note embeds `![[photo.png]]`. To add an image, PDF, or video, call `create_attachment` with base64 bytes (or PUT the raw bytes to /attachments/{path} with the bearer token — required for video, which is too big to base64); `delete_attachment` and `move_attachment` mirror the note tools. Every attachment tool returns `url` (view inline) and `download_url` (save the file); both are short-lived. Video is never returned as bytes — hand the user its download_url."

type Deps struct {
	Store *vault.Store
	Token string
	// PublicURL enables the minimal OAuth 2.1 flow for MCP clients that
	// cannot send a static bearer header (claude.ai custom connectors).
	PublicURL string
}

func NewHTTPServer(deps Deps) http.Handler {
	// Render Links (ADR-10) exist only when there is a public base URL to
	// mint against; the signing secret is boot-generated, so a restart
	// revokes every outstanding link.
	var signer *render.Signer
	if deps.PublicURL != "" {
		signer = render.NewSigner(deps.PublicURL)
	}
	mcpServer := newMCPServer(deps.Store, signer)
	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		return mcpServer
	}, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeHealth(w, deps.Store.Paths.Root)
	})
	var metadataURL string
	if deps.PublicURL != "" {
		oa := oauth.New(deps.PublicURL, deps.Token)
		oa.Register(mux)
		metadataURL = oa.ResourceMetadataURL()
		mux.HandleFunc("GET /render/{path...}", handleRender(deps.Store, signer))
	}
	mux.Handle("/mcp", auth.BearerMiddlewareWithMetadata(deps.Token, metadataURL, mcpHandler))
	if !deps.Store.ReadOnly {
		upload := auth.BearerMiddlewareWithMetadata(deps.Token, metadataURL, handleUpload(deps.Store, signer))
		mux.Handle("PUT /attachments/{path...}", upload)
		mux.Handle("POST /attachments/{path...}", upload)
	}

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

// newMCPServer builds the MCP surface. signer is nil when no public URL is
// configured; attachment tools then omit the url field.
func newMCPServer(store *vault.Store, signer *render.Signer) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{
		Name:    "horde",
		Version: Version,
	}, &mcp.ServerOptions{
		Instructions: Instructions,
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "hello",
		Description: "Connectivity check — returns a static greeting and the server version. Example: hello() → {message: \"hello from horde\"}. Touches nothing in the vault.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, helloOut, error) {
		return nil, helloOut{Message: "hello from horde", Version: Version}, nil
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
		Description: "List markdown notes under a path prefix (skips images/PDFs — use list_attachments for those). Example: list_notes({path_prefix: \"network\", recursive: true, sort: \"modified\", limit: 50}). Returns [{path, size, modified}] with root-relative paths.",
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
		Description: "Read a single markdown note. Example: read_note({path: \"network/opnsense/wan-failover.md\", include_frontmatter: true}). Large notes truncate at 100 KiB; use offset/length to page through the body.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in readNoteIn) (*mcp.CallToolResult, readNoteOut, error) {
		include := true
		if in.IncludeFrontmatter != nil {
			include = *in.IncludeFrontmatter
		}
		note, err := store.ReadNote(in.Path, include, in.Offset, in.Length)
		if err != nil {
			return nil, readNoteOut{}, err
		}
		out := readNoteOut{
			Path:               note.Path,
			Body:               note.Body,
			Size:               note.Size,
			Modified:           note.Modified,
			Offset:             note.Offset,
			Truncated:          note.Truncated,
			HasConflictMarkers: note.HasConflictMarkers,
		}
		if include && note.Frontmatter != nil {
			out.Frontmatter = note.Frontmatter
		}
		return nil, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "search_notes",
		Description: "Full-text search across markdown notes only (does not search image filenames or binary content — use list_attachments). Example: search_notes({query: \"opnsense deco\", path_prefix: \"network\", limit: 20}). Space-separated terms are ANDed. Returns [{path, line, snippet, match_count, score}].",
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

	mcp.AddTool(s, &mcp.Tool{
		Name:        "history",
		Description: "Git commit history for one note. Example: history({path: \"network/opnsense/wan-failover.md\", limit: 10}). Returns [{hash, message, author, date}] newest first.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in historyIn) (*mcp.CallToolResult, historyOut, error) {
		entries, err := store.NoteHistory(in.Path, in.Limit)
		if err != nil {
			return nil, historyOut{}, err
		}
		return nil, historyOut{Commits: entries}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "link_graph",
		Description: "Parse [[wikilinks]] across one wiki and return the adjacency map for lint (orphans, broken links). Example: link_graph({wiki: \"network\"}) → {pages: [{path, out_links, in_link_count}], orphans, broken_links}. Does not follow or rewrite links.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in linkGraphIn) (*mcp.CallToolResult, vault.LinkGraph, error) {
		g, err := store.LinkGraph(in.Wiki)
		if err != nil {
			return nil, vault.LinkGraph{}, err
		}
		return nil, *g, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_attachments",
		Description: "REQUIRED to discover images/PDFs — list_notes and search_notes skip binaries. Enumerate allowlisted attachments (png/jpg/jpeg/gif/webp/pdf by default) under a path prefix. Example: list_attachments({path_prefix: \"Pegasus\"}) → [{uri, path, name, mime_type, size, modified, url}]. Then call read_attachment with path, or resources/read on the returned wiki:// URI (URIs are percent-encoded). url and download_url, when present, are short-lived (~6 h) links — url displays the file inline in chat, download_url saves it to disk. Neither is a permalink or share link. Covers images, PDFs, and video (mp4/webm/mov).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in listAttachmentsIn) (*mcp.CallToolResult, listAttachmentsOut, error) {
		atts, err := store.ListAttachments(in.PathPrefix, in.Limit)
		if err != nil {
			return nil, listAttachmentsOut{}, err
		}
		out := make([]attachmentMetaOut, len(atts))
		for i, att := range atts {
			out[i] = attachmentMetaOut{AttachmentMeta: att}
			if signer != nil {
				out[i].URL = signer.Mint(att.Path)
				out[i].DownloadURL = downloadURL(out[i].URL)
			}
		}
		return nil, listAttachmentsOut{Attachments: out}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "read_attachment",
		Description: "Fetch an allowlisted attachment (read-only). Example: read_attachment({path: \"Pegasus/90 Assets/Photos/pegasus.png\"}) or a wiki:// URI from list_attachments. Prefer the path field from list_attachments. Returns image content (for small image/*) plus mime_type/path metadata, and sets inlined: true when it did. Video, and anything over HORDE_MAX_ATTACHMENT_BYTES, comes back as metadata with inlined: false — give the user url (view inline, ~6 h) or download_url (save the file) instead of trying to fetch bytes. Neither link is a permalink or share link. Rejects disallowed extensions and excluded dirs.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in readAttachmentIn) (*mcp.CallToolResult, readAttachmentOut, error) {
		// Stat first: video and anything past the inline cap is returned as
		// links rather than bytes, so a 200 MB clip answers with a URL
		// instead of failing or trying to base64 itself into the response.
		meta, err := store.StatAttachment(in.Path)
		if err != nil {
			return nil, readAttachmentOut{}, err
		}
		out := readAttachmentOut{
			URI:      meta.URI,
			Path:     meta.Path,
			MIMEType: meta.MIMEType,
			Size:     meta.Size,
		}
		if signer != nil {
			out.URL = signer.Mint(meta.Path)
			out.DownloadURL = downloadURL(out.URL)
		}
		if config.IsStreamOnlyMIME(meta.MIMEType) || meta.Size > store.MaxAttachmentBytes {
			if out.URL == "" {
				return nil, readAttachmentOut{}, fmt.Errorf(
					"%s is %d bytes (%s) — too large to return inline, and no HORDE_PUBLIC_URL is set to mint a download link",
					meta.Path, meta.Size, meta.MIMEType)
			}
			out.Note = "Bytes not inlined — open url to view, or download_url to save the file."
			return nil, out, nil
		}
		att, err := store.ReadAttachment(in.Path)
		if err != nil {
			return nil, readAttachmentOut{}, err
		}
		out.Inlined = true
		return &mcp.CallToolResult{
			Content: attachmentContent(att),
		}, out, nil
	})

	// Preferred fetch path: MCP resources with wiki:// URIs.
	readWikiResource := func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		att, err := store.ReadAttachment(req.Params.URI)
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      att.URI,
				MIMEType: att.MIMEType,
				Blob:     att.Bytes,
			}},
		}, nil
	}
	s.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "wiki://{wiki}/{+filepath}",
		Name:        "vault-attachment",
		Title:       "Vault attachment",
		Description: "Allowlisted attachment under HORDE_ROOT. URI example: wiki://network/diagrams/wan.png. Allowlist and size cap from HORDE_ATTACHMENT_EXTS / HORDE_MAX_ATTACHMENT_BYTES; video is too large to read this way — use read_attachment and hand the user its download_url. Note tools do not return binary bytes.",
		MIMEType:    "application/octet-stream",
	}, readWikiResource)

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

		mcp.AddTool(s, &mcp.Tool{
			Name:        "create_attachment",
			Description: "Upload an allowlisted attachment (png/jpg/jpeg/gif/webp/pdf by default) into a wiki. Example: create_attachment({path: \"Pegasus/90 Assets/Photos/rack.png\", content_base64: \"iVBORw0KGgo...\", overwrite: false}). content_base64 is standard base64 of the raw file bytes; the extension must match the allowlist and the decoded size must be under HORDE_MAX_ATTACHMENT_BYTES. For anything larger — video especially — PUT the raw bytes to /attachments/{path} with the bearer token instead, which streams and allows up to HORDE_MAX_STREAM_BYTES. Returns url and download_url for the stored file. Committed to git unless the vault gitignores it (then commit is empty and the nightly backup covers it); embed it in a note with ![[filename]].",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in createAttachmentIn) (*mcp.CallToolResult, createAttachmentOut, error) {
			start := time.Now()
			data, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(in.ContentBase64))
			if decErr != nil {
				logTool("create_attachment", []string{in.Path}, start, decErr, "", 0)
				return nil, createAttachmentOut{}, fmt.Errorf("content_base64 is not valid base64: %w", decErr)
			}
			res, err := store.WriteAttachment(in.Path, data, in.Overwrite)
			var commit string
			if res != nil {
				commit = res.Commit
			}
			logTool("create_attachment", []string{in.Path}, start, err, commit, resBytes(res))
			if err != nil {
				return nil, createAttachmentOut{}, err
			}
			out := createAttachmentOut{WriteResult: *res, URI: paths.WikiURI(res.Path)}
			if signer != nil {
				out.URL = signer.Mint(res.Path)
				out.DownloadURL = downloadURL(out.URL)
			}
			return nil, out, nil
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "delete_attachment",
			Description: "Delete an attachment. Git-recoverable only if the file is in git; gitignored binaries return an empty commit and are recoverable only from the vault backup. Example: delete_attachment({path: \"Pegasus/90 Assets/Photos/rack.png\", confirm: true}). confirm must be true. Does not touch notes that embed the file — their ![[links]] will simply break.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in deleteAttachmentIn) (*mcp.CallToolResult, vault.DeleteResult, error) {
			if !in.Confirm {
				return nil, vault.DeleteResult{}, fmt.Errorf("confirm must be true")
			}
			start := time.Now()
			res, err := store.DeleteAttachment(in.Path)
			var commit string
			if res != nil {
				commit = res.Commit
			}
			logTool("delete_attachment", []string{in.Path}, start, err, commit, 0)
			if err != nil {
				return nil, vault.DeleteResult{}, err
			}
			return nil, *res, nil
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "move_attachment",
			Description: "Rename or move an attachment. Does not rewrite the ![[embeds]] pointing at it. Example: move_attachment({from: \"Pegasus/inbox/rack.png\", to: \"Pegasus/90 Assets/Photos/rack.png\", overwrite: false}). Both paths must be allowlisted attachment types inside a wiki.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in moveAttachmentIn) (*mcp.CallToolResult, vault.MoveResult, error) {
			start := time.Now()
			res, err := store.MoveAttachment(in.From, in.To, in.Overwrite)
			var commit string
			if res != nil {
				commit = res.Commit
			}
			logTool("move_attachment", []string{in.From, in.To}, start, err, commit, 0)
			if err != nil {
				return nil, vault.MoveResult{}, err
			}
			return nil, *res, nil
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "restore",
			Description: "Restore a note from a git ref and commit the result. Example: restore({path: \"network/opnsense/wan-failover.md\", ref: \"abc1234\"}). Use history to find refs.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, in restoreIn) (*mcp.CallToolResult, vault.WriteResult, error) {
			start := time.Now()
			res, err := store.RestoreNote(in.Path, in.Ref)
			var commit string
			if res != nil {
				commit = res.Commit
			}
			logTool("restore", []string{in.Path}, start, err, commit, resBytes(res))
			if err != nil {
				return nil, vault.WriteResult{}, err
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

type helloOut struct {
	Message string `json:"message"`
	Version string `json:"version"`
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
	Offset             int64  `json:"offset,omitempty" jsonschema:"Byte offset into the body (or full file when frontmatter is disabled)"`
	Length             int64  `json:"length,omitempty" jsonschema:"Max bytes to return; defaults to 100 KiB"`
}

type readNoteOut struct {
	Path               string         `json:"path"`
	Frontmatter        map[string]any `json:"frontmatter,omitempty"`
	Body               string         `json:"body"`
	Size               int64          `json:"size"`
	Modified           time.Time      `json:"modified"`
	Offset             int64          `json:"offset,omitempty"`
	Truncated          bool           `json:"truncated,omitempty"`
	HasConflictMarkers bool           `json:"has_conflict_markers,omitempty"`
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

type historyIn struct {
	Path  string `json:"path" jsonschema:"Root-relative path to the note"`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum commits to return (default 20)"`
}

type historyOut struct {
	Commits []git.HistoryEntry `json:"commits"`
}

type linkGraphIn struct {
	Wiki string `json:"wiki" jsonschema:"Top-level wiki folder name"`
}

type listAttachmentsIn struct {
	PathPrefix string `json:"path_prefix,omitempty" jsonschema:"Root-relative prefix; empty means vault root"`
	Limit      int    `json:"limit,omitempty" jsonschema:"Maximum attachments to return"`
}

// attachmentMetaOut adds the Render Link (ADR-10) to the vault metadata;
// URL is empty (and omitted) when HORDE_PUBLIC_URL is not set.
type attachmentMetaOut struct {
	vault.AttachmentMeta
	URL         string `json:"url,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

type listAttachmentsOut struct {
	Attachments []attachmentMetaOut `json:"attachments"`
}

type readAttachmentIn struct {
	Path string `json:"path" jsonschema:"Root-relative path or wiki:// URI"`
}

type readAttachmentOut struct {
	URI      string `json:"uri"`
	Path     string `json:"path"`
	MIMEType string `json:"mime_type"`
	Size     int64  `json:"size"`
	URL      string `json:"url,omitempty"`
	// DownloadURL is the same link with ?download=1, which saves the file
	// instead of rendering it.
	DownloadURL string `json:"download_url,omitempty"`
	// Inlined is false when the bytes were too large (or video) to return in
	// the response; the caller fetches them through the links instead.
	Inlined bool   `json:"inlined"`
	Note    string `json:"note,omitempty"`
}

// attachmentContent prefers MCP image content for image/* so Claude connectors
// can display bytes; other allowlisted types stay as embedded resources.
func attachmentContent(att *vault.AttachmentRead) []mcp.Content {
	if strings.HasPrefix(att.MIMEType, "image/") {
		return []mcp.Content{
			&mcp.ImageContent{Data: att.Bytes, MIMEType: att.MIMEType},
		}
	}
	return []mcp.Content{
		&mcp.EmbeddedResource{
			Resource: &mcp.ResourceContents{
				URI:      att.URI,
				MIMEType: att.MIMEType,
				Blob:     att.Bytes,
			},
		},
	}
}

type restoreIn struct {
	Path string `json:"path" jsonschema:"Root-relative path to restore"`
	Ref  string `json:"ref" jsonschema:"Git commit hash or ref"`
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

type createAttachmentIn struct {
	Path          string `json:"path" jsonschema:"Root-relative path inside a wiki, ending in an allowlisted attachment extension"`
	ContentBase64 string `json:"content_base64" jsonschema:"Standard base64 encoding of the raw file bytes"`
	Overwrite     bool   `json:"overwrite,omitempty" jsonschema:"Overwrite if the attachment already exists"`
}

type createAttachmentOut struct {
	vault.WriteResult
	URI         string `json:"uri"`
	URL         string `json:"url,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
}

type deleteAttachmentIn struct {
	Path    string `json:"path" jsonschema:"Root-relative path to delete"`
	Confirm bool   `json:"confirm" jsonschema:"Must be true to confirm deletion"`
}

type moveAttachmentIn struct {
	From      string `json:"from" jsonschema:"Source path"`
	To        string `json:"to" jsonschema:"Destination path"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"Overwrite destination if it exists"`
}

func NewStore(opts StoreOpts) (*vault.Store, error) {
	pv, err := paths.NewVault(opts.Root, opts.Ignore)
	if err != nil {
		return nil, err
	}
	maxNote := opts.MaxNoteBytes
	if maxNote <= 0 {
		maxNote = 2 * 1024 * 1024
	}
	maxAtt := opts.MaxAttachmentBytes
	if maxAtt <= 0 {
		maxAtt = config.DefaultMaxAttachmentBytes
	}
	maxStream := opts.MaxStreamBytes
	if maxStream <= 0 {
		maxStream = config.DefaultMaxStreamBytes
	}
	return &vault.Store{
		Paths:              pv,
		MaxNoteBytes:       maxNote,
		MaxAttachmentBytes: maxAtt,
		MaxStreamBytes:     maxStream,
		AttachmentMIME:     opts.AttachmentMIME,
		ReadLimitBytes:     vault.DefaultReadLimit,
		Git:                opts.Git,
		ReadOnly:           opts.ReadOnly,
	}, nil
}

// StoreOpts configures a vault.Store.
type StoreOpts struct {
	Root               string
	Ignore             []string
	MaxNoteBytes       int64
	MaxAttachmentBytes int64
	MaxStreamBytes     int64
	AttachmentMIME     map[string]string
	Git                *git.Repo
	ReadOnly           bool
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
