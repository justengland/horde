# horde — Obsidian Vault MCP Server (Go)

**Status:** v1.6 · M6 complete (render links) · updated 2026-07-13
**Owner:** Justin
**Goal:** Expose an Obsidian vault (plain markdown directory) as a remote MCP server so Claude.ai / Claude mobile can read, search, create, edit, and delete notes, with git as the safety net.

---

## 1. Overview

A standalone Go binary that serves the Model Context Protocol over **Streamable HTTP**, operating directly on the vault filesystem. The vault lives on Justin's desktop (24/7), which is the sole source of truth; the only write paths are Obsidian on that desktop and horde itself. The server is published to the internet via a **Cloudflare Tunnel**; auth is enforced at both the Cloudflare edge and in the server itself. Every mutation is committed to git automatically, so any change Claude makes is inspectable and revertible.

```
Claude (Anthropic cloud)
   │  HTTPS  (OAuth 2.1 handshake → Authorization: Bearer <token>)
   ▼
Cloudflare edge  (TLS termination for horde.crucibleplatform.com)
   │  cloudflared tunnel (outbound-only from lab; sidecar container)
   ▼
horde (Go, Docker; host-published on 127.0.0.1:8787)  ── validates bearer token
   │  fs read/write (atomic)
   ▼
~/Documents/Obsidian  (git repo; sole copy — replicated only via git hub + rclone)
```

Non-goals for v1: multi-vault support, multi-user auth, wikilink graph analysis, attachment/binary serving, Obsidian plugin/UI features (canvas rendering, dataview execution). Graph + read-only attachments land in v1.5 (§7, §14); signed render links land in v1.6 (§7, ADR-10); attachment uploads, video, and downloads land in v1.7 (§7, ADR-11/12); canvas rendering and dataview stay out of scope.

## 2. Stack & Dependencies

| Concern | Choice | Notes |
|---|---|---|
| Language | Go 1.23+ | Single static binary |
| MCP SDK | `github.com/modelcontextprotocol/go-sdk` | Official SDK; Streamable HTTP transport |
| Git | `os/exec` → system `git` | Simpler and more robust than go-git for commit/log/restore; vault machine already has git |
| Search | Pure-Go concurrent scan | No index, no `rg` dependency; see §7 `search_notes` |
| Frontmatter | `gopkg.in/yaml.v3` | Parse/emit YAML frontmatter |
| Config | env vars + optional YAML file | Twelve-factor-ish; systemd `EnvironmentFile` |
| Logging | `log/slog` JSON | Audit every tool call |

## 3. Transport & Endpoints

- `POST/GET /mcp` — MCP Streamable HTTP endpoint (SDK-provided handler).
- `GET /healthz` — liveness: returns vault path readable, git repo OK, version. Unauthenticated by design.
- OAuth 2.1 endpoints (enabled when `HORDE_PUBLIC_URL` is set; see §4): `/.well-known/oauth-protected-resource[/mcp]`, `/.well-known/oauth-authorization-server`, `/oauth/register`, `/oauth/authorize`, `/oauth/token`.
- Binding: inside the container horde binds `0.0.0.0:8787` so the cloudflared sidecar can reach it over the compose network; the host port is published on `127.0.0.1` only. Nothing listens on LAN/WAN.
- Exactly one public-but-signed attachment route (v1.6, ADR-10): `GET /render/{path}?exp=<unix>&sig=<hmac>[&download=1]` serves Render Links minted by the attachment tools. It streams (`http.ServeContent`), so it answers Range requests — video seeking and resumed downloads — and `download=1` flips `Content-Disposition` from `inline` to `attachment` (v1.7, ADR-12). No other static-file routes; every other fetch path stays MCP-only behind the bearer/OAuth gate (§7).
- One bearer-gated upload route (v1.7, ADR-11): `PUT|POST /attachments/{path...}?overwrite=<bool>` streams raw request-body bytes to disk as an attachment, applying exactly the rules `create_attachment` applies. Never signed-and-public — a leaked read link costs one file for six hours, a leaked write link costs the vault. Absent entirely when `HORDE_READONLY` is set.
- Session handling per SDK defaults; server is effectively stateless between calls (no in-memory vault index in v1).

## 4. Authentication

Two independent layers; both must pass.

**Layer 1 — Cloudflare edge.** A WAF custom rule on the tunnel hostname: block any request where `http.request.headers["authorization"][0]` does not equal `Bearer <token>` (or, minimally, where the header is absent). This drops internet noise before it ever reaches the lab.

> **Why not Cloudflare Access service tokens:** Claude's custom-connector request headers are restricted to an allowlist (`authorization`, `x-api-key`, `x-auth-token`). CF Access service tokens require `CF-Access-Client-Id` / `CF-Access-Client-Secret` headers, which Claude cannot send. The WAF-rule approach gets equivalent edge filtering using the `authorization` header Claude *can* send.

**Layer 2 — horde.** Middleware validates `Authorization: Bearer <token>` with constant-time compare against `HORDE_TOKEN`. 401 otherwise. Token: 32+ bytes random, base64url.

**Connector wiring (as shipped).** The request-header option was not available; claude.ai custom connectors require the OAuth 2.1 handshake, so horde implements the minimal single-user flow (`internal/oauth`, ADR-9): RFC 9728/8414 discovery, dynamic client registration, authorization code + PKCE (S256, single-use codes, 5-min TTL). The authorize page is a one-field form where Justin pastes `HORDE_TOKEN`; the issued access token *is* `HORDE_TOKEN`, so Layer 2 above is unchanged and no token state is lost on restart. claude.ai's callback hosts are allowlisted so a restart (which wipes in-memory client registrations) can't strand a connected client. Connector setup: add `https://horde.crucibleplatform.com/mcp` as a custom connector with OAuth client ID/secret left blank.

> **WAF caveat:** the Layer-1 rule must be scoped to the `/mcp` path — `/oauth/*` and `/.well-known/*` are hit by claude.ai *before* it has a token, and `/healthz` is intentionally public.

## 5. Data Model — Wikis

- `HORDE_ROOT` = `~/Documents/Obsidian/` (absolute path, a git repo).
- **Every top-level folder under the root is an independent wiki** (e.g. `network/`, `pegasus/`, `recipes/`). Horde treats them as namespaces; there is no cross-wiki implicit behavior.
- **Each wiki carries a `taxonomy.md` at its root** describing that wiki's structure, naming, frontmatter, and editing conventions. This file is the contract for how agents edit that wiki. Wikis follow the llm-wiki pattern (Karpathy): per-wiki `index.md` (routing catalog, read before querying), `log.md` (append-only, greppable `## [date] op | title` entries), optional immutable `raw/` sources, and periodic lint passes — all defined in the taxonomy, not in horde. Horde stays a dumb, safe file server; wiki discipline lives in the schema files it serves.
- **Taxonomy enforcement flow:** the server's MCP `instructions` (initialize response) state: *"Before creating or editing notes in a wiki, read `<wiki>/taxonomy.md` and follow it. If it does not exist, propose one using `create_note` before making other changes."* Lazy-read keeps the instructions payload small regardless of wiki count.
- A `list_wikis` tool (see §7) enumerates top-level wikis with note counts and whether a `taxonomy.md` exists, so Claude can orient in one call.
- **Writable extensions:** `.md` only — through v1.5 and beyond unless explicitly revisited. No binary writes via horde.
- **Readable notes (v1):** `.md` fully; `.canvas` read-only as text/JSON (no canvas rendering).
- **Readable attachments (v1.5):** allowlisted binaries (images, PDF, etc.) via MCP resources / `read_attachment` — see §7. v1.6 adds Render Links: short-lived signed URLs on the same attachments for inline display in chat clients (§7, ADR-10). Note tools (`list_notes`, `search_notes`, `read_note`) stay markdown-focused; they do not return attachment bytes.
- **Excluded from all operations:** `.obsidian/`, `.git/`, `.trash/`, loose files at the root itself (notes must live inside a wiki), plus configurable ignore globs.
- Paths in the API are always root-relative with forward slashes and must begin with a wiki name (`network/opnsense/wan-failover.md`).
- Frontmatter is parsed when present and returned as structured data alongside body content.

### Path safety (hard requirements)
1. Clean and resolve every input path; reject anything escaping `HORDE_ROOT` after `filepath.EvalSymlinks` (defeats `..` and symlink tricks).
2. Reject absolute paths, empty paths, paths into excluded dirs, and disallowed extensions on write (and, for v1.5 attachment reads, disallowed extensions on read).
3. Max path length 255 bytes/segment; UTF-8 validated.

### Write safety
- Atomic writes: write to `.<name>.tmp-<rand>` in the same directory, fsync, rename. No torn files even if the box loses power or a backup job reads mid-write.
- Max note size 2 MiB (configurable); reject larger writes.
- `update_note` patch mode requires `old_str` to be unique in the file (str-replace semantics) — fails loudly rather than guessing.

## 6. Git Safety Net

- Precondition at startup: `HORDE_ROOT` is a git repo with a clean-enough state (untracked files OK). Refuse to start otherwise unless `HORDE_GIT=off`.
- **Every mutating tool call** runs, after a successful write:
  `git add -- <path> && git commit -m "mcp(<tool>): <path>" --author "horde <mcp@pluto>"` (explicit paths only — never `-A`; see ADR-4)
- Deletes are **soft**: `delete_note` does `git rm` + commit (recoverable via git), never `rm` of untracked content without a prior `git add`.
- Optional `HORDE_GIT_PUSH=1`: async `git push` after commit, best-effort with backoff; failures logged, never block the tool response.
- v1.5 candidates: `link_graph` tool; read-only attachment serving (§7).

### Backup strategy (git vs rclone)
Git and the nightly rclone→iCloud job solve different problems and both stay (ADR-13):
- **Git** = versioning and blame for **notes** (`.md`, `.canvas`). The vault `.gitignore` keeps binaries out; horde skips git for ignored paths (empty `commit`).
- **rclone→iCloud** (`obsidian-backup.timer`, 02:30 nightly, `apples:horde-backup/`) = disaster recovery for the whole vault: binaries, `.obsidian/`, and `.git/`. `--backup-dir versions/<date>` keeps anything sync would delete or overwrite, which is the only recovery path for a deleted binary.

## 7. Tools

All tools return structured content; errors are MCP tool errors with actionable messages.

### `list_wikis`
Orientation call — enumerate the wikis.
- **In:** none
- **Out:** array of `{name, note_count, has_taxonomy (bool), modified}` for each top-level folder under `HORDE_ROOT` (excluded dirs omitted).

### `search_notes`
Full-text + filename search. **Design: stateless brute-force scan, no index.** Rationale: the vault (~thousands of files, tens of MB) scans in tens of ms warm / low hundreds cold — below the MCP round-trip latency floor — and Obsidian writes to the vault behind horde's back, so any persistent index (Bleve, SQLite FTS) would need watcher/invalidation machinery to stay coherent. Stateless scan is always correct by construction.

- **In:** `query` (string, required — space-separated terms treated as implicit AND), `path_prefix` (string), `limit` (int, default 20, max 100), `case_sensitive` (bool, default false), `regex` (bool, default false — compiles query via stdlib `regexp`, `(?i)` unless case_sensitive)
- **Out:** array of `{path, line, snippet, match_count, score}`. Snippet = matched line ±1 context, ≤200 chars, with line number so `update_note(patch)` can target it.
- **Impl:**
  - `filepath.WalkDir` from `path_prefix`, pruning excluded dirs at directory level; paths fed over a channel to `runtime.NumCPU()` workers using `bufio.Scanner`.
  - AND semantics: all terms must appear somewhere in the file; snippet taken from the first term's first match. Optimizes for recall — Claude retries with varied terms, so recall beats clever ranking.
  - Cheap tiered scoring for ordering only: filename match > `#` heading match > frontmatter match > body match; modified-time as tiebreaker. (~15 lines, not a ranking engine.)
  - Bounded work: bail per-file after N matches, stop globally at `limit`; frontmatter is included in the scanned text.
  - Skip files >`HORDE_MAX_NOTE_BYTES` and binary-looking content (NUL sniff in first 512 B).

### `list_notes`
- **In:** `path_prefix` (string, default vault root), `recursive` (bool, default true), `sort` (`name|modified`, default `modified`), `limit` (default 200)
- **Out:** array of `{path, size, modified}`; directories implied by paths.

### `read_note`
- **In:** `path` (required), `include_frontmatter` (bool, default true), `offset` (int, default 0), `length` (int, default 100 KiB)
- **Out:** `{path, frontmatter (object|null), body, size, modified, offset, truncated, has_conflict_markers}`. Body is paged via `offset`/`length`; frontmatter is always returned whole when enabled. Conflict markers are flagged but not auto-resolved.

### `create_note`
- **In:** `path` (required, must end `.md`), `content` (required), `overwrite` (bool, default **false**)
- **Out:** `{path, created: true, commit}`. Creates parent directories. Fails if exists and `overwrite=false`.

### `update_note`
- **In:** `path` (required), `mode` (`patch|append|prepend|replace`), and per-mode fields: `old_str`+`new_str` (patch), `content` (others)
- **Out:** `{path, mode, commit, bytes_written}`
- `patch` is the documented-preferred mode in tool description (nudges Claude toward surgical edits).

### `delete_note`
- **In:** `path` (required), `confirm` (bool, required, must be `true`)
- **Out:** `{path, deleted: true, commit, restore_hint: "git checkout <commit>^ -- <path>"}`

### `move_note`
- **In:** `from`, `to` (both required), `overwrite` (default false)
- **Out:** `{from, to, commit}`. v1 does **not** rewrite wikilinks; the tool description says so explicitly so Claude can warn or fix links itself via search+patch.

### `history`
Git commit log for one note.
- **In:** `path` (required), `limit` (int, default 20)
- **Out:** `{commits: [{hash, message, author, date}]}` newest first.

### `restore`
Check out a note at a git ref and commit the result.
- **In:** `path` (required), `ref` (required — commit hash or ref)
- **Out:** `{path, commit, bytes_written}`. Mutating tools reject notes with unresolved conflict markers.

### `link_graph` (v1.5 — required for lint at scale)
Parse `[[wikilinks]]` across one wiki and return the adjacency map without the agent reading every page body.
- **In:** `wiki` (required)
- **Out:** `{pages: [{path, out_links, in_link_count}], orphans: [path], broken_links: [{path, target}]}`
- Powers the lint pass's orphan and broken-link checks in one call instead of N reads.

### Attachments — read-only binary serving (v1.5)

**Why:** Vaults hold images and PDFs next to notes (`![[shot.png]]`, `attachments/…`). Today Claude can only touch `.md` / `.canvas`; without a fetch path, image-bearing notes are half-blind. This is the missing read surface for Claude usability — not an Obsidian feature port.

**Transport shape (preferred):** MCP **resources** with `wiki://` URIs (e.g. `wiki://network/diagrams/wan.png`). Clients that support resources can discover and fetch without a bespoke tool. **Fallback:** a `read_attachment` tool with the same safety rules if Claude connectors prove awkward with resources. Implement one path first; do not ship both indefinitely without a reason.

**HTTP surface (amended by ADR-10, 2026-07-13).** v1.5 shipped fetch as MCP-only — no static files, everything behind the bearer/OAuth gate. That held until a client limitation surfaced: claude.ai does not render `ImageContent` blocks from connector tool results, so image-bearing notes stay invisible in chat. v1.6 adds exactly one HTTP route as a render helper — **Render Links**: when `HORDE_PUBLIC_URL` is set, `list_attachments` / `read_attachment` include a `url` field, an HMAC-signed link (`GET /render/{path}?exp=<unix>&sig=<hmac>`) expiring ~6 hours after minting. The route serves precisely what `read_attachment` serves — same allowlist, size cap, and path safety — and sends `Cache-Control: private, no-store` so Cloudflare's edge cache can't outlive expiry. The signing secret is generated at boot; a restart revokes every outstanding link. Links live only in chat responses (stale images in old conversations are accepted); this is not a file server, not a share feature, and attachments are still never served unsigned.

#### Discovery
- **Resources list** (or `list_attachments` if tool-shaped): enumerate allowlisted files under an optional `path_prefix` / wiki, returning `{uri|path, name, mime_type, size, modified}`.
- Prune excluded dirs (§5); skip non-allowlisted extensions; do not recurse into `.obsidian` / `.git` / `.trash`.
- Note tools stay as they are: `list_notes` / `search_notes` continue to skip non-`.md` (and do not index binary content). Attachment discovery is a separate call so agents opt in.

#### Fetch
- **Resources read** (or `read_attachment`): given a root-relative path or `wiki://` URI, return **raw bytes** plus **correct MIME** (from extension allowlist map; sniff only as a soft check, never to expand the allowlist).
- Cap response size at `HORDE_MAX_ATTACHMENT_BYTES` (default 5 MiB); reject larger files with an actionable error (path + size + cap) rather than truncating binary payloads.
- Same path-safety hard requirements as §5: clean/resolve, no escape from `HORDE_ROOT` after `EvalSymlinks`, reject absolute paths, empty paths, and excluded dirs.

#### Allowlist (default)
| Ext | MIME |
|---|---|
| `.png` | `image/png` |
| `.jpg` / `.jpeg` | `image/jpeg` |
| `.gif` | `image/gif` |
| `.webp` | `image/webp` |
| `.pdf` | `application/pdf` |
| `.mp4` / `.m4v` | `video/mp4` |
| `.webm` | `video/webm` |
| `.mov` | `video/quicktime` |

Configurable via `HORDE_ATTACHMENT_EXTS` (comma-sep extensions, no dots). Adding a type is a config change + MIME map entry — not an implicit "serve anything".

#### Attachment uploads (v1.7, amends the "no binary writes" rule — ADR-11)

**Why:** v1.5 assumed binaries only ever arrive from Obsidian or a human. They also arrive from the agent — a generated diagram, a screenshot handed to it, a PDF worth filing next to the note that cites it — and the read-only rule forced a trip outside the session to file each one.

**Two transports, one rule set.** `create_attachment` takes `content_base64` (in-session, any MCP client, but ~33% base64 overhead — small files); `PUT|POST /attachments/{path...}` streams the raw body to disk behind the bearer gate (efficient, and the only path for video). Both funnel through one write path enforcing the configured allowlist, `HORDE_MAX_ATTACHMENT_BYTES`, the same path safety as reads (inside a wiki, no traversal, no excluded dirs), one git commit per write, and refusal under `HORDE_READONLY`. Uploads are immediately visible to `list_attachments` / `read_attachment` / Render Links.

**Parity with the note tools.** `delete_attachment` (git-recoverable, `confirm` required) and `move_attachment` (does not rewrite the `![[embeds]]` pointing at the file, exactly like `move_note`). No new config knob: `HORDE_READONLY` is the write switch, and the extension allowlist is now a write-side gate as much as a read-side one.

#### Video + downloads (v1.7, ADR-12)

**Why:** vaults hold clips and screen recordings, and "view this" is not "keep this". The v1.6 route buffered whole files, served them with no `Content-Disposition`, and the allowlist stopped at PDF.

- **Video in the allowlist:** `mp4`, `m4v`, `webm`, `mov` ship in the defaults. The allowlist gates uploads too, so video can be filed as well as fetched.
- **The route streams** via `http.ServeContent`, which brings Range with it — a browser's `<video>` seeks with Range, and an interrupted download resumes with it. `Cache-Control: private, no-store` still applies to ranged responses.
- **Two caps, split by mechanism:** `HORDE_MAX_ATTACHMENT_BYTES` (5 MB) bounds what may be held in memory and base64'd into an MCP response; `HORDE_MAX_STREAM_BYTES` (512 MB) bounds the streaming HTTP paths (download and upload).
- **`url` + `download_url`** on every attachment tool: the same signed link, `download_url` adding `?download=1` for `Content-Disposition: attachment` plus filename. The signature covers path and expiry only, so the parameter grants no extra access.
- **`read_attachment` stats first:** video and anything past the inline cap return metadata and links with `inlined: false`, rather than failing or base64-ing a 200 MB clip into a JSON-RPC response.

#### Out of scope (still)
- **No canvas rendering** — `.canvas` stays readable as text only; horde does not rasterize or interpret canvas graphs.
- **No Obsidian plugin features** — no dataview, no embed resolution beyond "here are the bytes at this path", no vault-wide media library UI.
- **No CDN** — no edge caching of vault bytes; `Cache-Control: no-store` is leak control, not a caching feature. (v1.7 does add Range support to the render route, for video seeking and resumable downloads — ADR-12 — but there is still no CDN, no transcoding, and no thumbnailing.)

### Deferred (v2)
`update_links_on_move`, `daily_note`, vault-wide `replace_all`. (Attachment/`wiki://` resources moved to v1.5 above.)

## 8. Tool Descriptions (prompt surface)

Descriptions ship in the binary and matter as much as code. Requirements: state path conventions (root-relative, must start with a wiki name), state that `patch` is preferred over `replace`, state that deletes are git-recoverable, and state that `move_note` doesn't fix links. Include one example call per tool in the description. For v1.5 attachment resources / `read_attachment`, state the allowlist, size cap, read-only nature, and that note tools do not return binary bytes. For v1.6, state that `url` is a short-lived Render Link (~6 h) for inline display only — not a permalink or share link. For v1.7, state that `download_url` saves the file while `url` displays it, that video is never inlined, the base64 encoding and the raw-body HTTP alternative for writes, that the allowlist and size cap apply to writes, that deletes are git-recoverable, and that `move_attachment` does not fix `![[embeds]]`.

## 9. Configuration

| Env | Default | Purpose |
|---|---|---|
| `HORDE_ROOT` | `~/Documents/Obsidian` | Root containing wiki folders |
| `HORDE_TOKEN` | — (required) | Bearer token |
| `HORDE_ADDR` | `127.0.0.1:8787` | Listen address |
| `HORDE_PUBLIC_URL` | — | External base URL; enables OAuth endpoints (§4) and Render Links (§7, v1.6) |
| `HORDE_READONLY` | `0` | Disable all mutating tools |
| `HORDE_GIT` | `on` | `on|off` |
| `HORDE_GIT_PUSH` | `0` | Async push after commit |
| `HORDE_IGNORE` | — | Extra ignore globs, comma-sep |
| `HORDE_MAX_NOTE_BYTES` | `2097152` | Write size cap |
| `HORDE_MAX_ATTACHMENT_BYTES` | `5242880` | Cap for in-memory attachment paths: MCP inline reads (v1.5) and base64 writes (v1.7) |
| `HORDE_MAX_STREAM_BYTES` | `536870912` | Cap for the streaming HTTP paths — download route and upload route (v1.7) |
| `HORDE_ATTACHMENT_EXTS` | `png,jpg,jpeg,gif,webp,pdf,mp4,webm,mov,m4v` | Allowlisted attachment extensions — gates reads (v1.5) and uploads (v1.7) |

Attachment uploads (v1.7) deliberately add no config either: `HORDE_READONLY` is the write switch and the existing allowlist/cap bound what may be written — see ADR-11. Render Links (v1.6) deliberately add no config: the TTL is a fixed ~6 h and the signing secret is generated at boot (a restart revokes all outstanding links) — see ADR-10.

## 10. Observability

- `slog` JSON to stdout (journald). One line per tool call: tool, path(s), duration, bytes, result, commit hash, remote session id. No note *content* in logs.
- `/healthz` for uptime-kuma or similar.
- Later: OTLP/Datadog exporter behind a build tag.

## 11. Deployment

- Docker compose (per ADR-1/ADR-3): multi-stage `Dockerfile` (`CGO_ENABLED=0` build → alpine + git, committer identity and `safe.directory=/vault` baked into the system gitconfig), `compose.yml` with the `horde` service (`user: 1000:1000`, vault bind-mounted at `/vault`, healthcheck on `/healthz`) and a `cloudflared` sidecar behind the `tunnel` profile.
- Secrets: `.env` is dotenvx-encrypted (`.env.keys` never committed); all compose invocations go through `dotenvx run --`. Make targets: `up`, `up-tunnel`, `down`, `logs`, `hello`.
- `cloudflared` ingress (Zero Trust dashboard): public hostname `horde.crucibleplatform.com` → service **HTTP** `horde:8787` (compose service name; the cloudflared→horde hop is plain HTTP on the private compose network — TLS terminates at the Cloudflare edge and the tunnel itself is QUIC-encrypted). Catch-all 404.
- Cloudflare WAF custom rule per §4 (scoped to `/mcp` — see caveat there). Zone: enable "Always Use HTTPS".
- README covers: token generation, connector setup on claude.ai web, restore-from-git recipes.

## 12. Risks & Open Questions

1. ~~**Request-header beta availability.**~~ **Resolved 2026-07-12:** header auth was indeed unavailable; the OAuth 2.1 fallback was implemented in horde itself (§4, ADR-9) and verified end-to-end from claude.ai.
2. **Concurrent interactive edits** (Obsidian save racing a horde write on the same file): atomic rename limits damage to last-write-wins on whole-file content; accepted for v1 per ADR-5's residual-risk note.
4. **Rate/abuse:** single-user, token-gated; no rate limiting in v1 beyond Cloudflare defaults.
5. **Render-link leakage (v1.6):** a leaked URL exposes one attachment for at most ~6 h; accepted per ADR-10. Emergency revocation = restart horde (regenerates the signing secret).

## 13. Continuous Improvement Loop

The system is designed to compound, not just persist. Three loops, smallest to largest:

**Loop 1 — Every query improves the wikis.** Taxonomy rule (enforced via horde's instructions): good answers get filed back as pages. A comparison, an analysis, a connection discovered while answering — `create_note` it, index it, log it. Explorations compound instead of evaporating into chat history.

**Loop 2 — Scheduled lint keeps the graph healthy.** Drift (under-updated cross-references, stale claims, orphans) is the #1 failure mode of an LLM-maintained wiki. Countermeasures:
- A recurring lint prompt (Claude scheduled task, or cron on the lab box hitting the API) runs the taxonomy's Lint operation per wiki — weekly per active wiki is the starting cadence.
- `link_graph` gives lint its orphan/broken-link checks in one tool call; `list_wikis` + `log.md` grep gives it staleness signals; the report is filed into the wiki (`lint-reports/YYYY-MM-DD.md`) and logged.
- Lint proposes and applies unambiguous fixes; destructive actions are flagged for Justin, never taken unilaterally.

**Loop 3 — The schemas and horde itself co-evolve.**
- `taxonomy.md` files are living: when an agent notices a convention repeatedly failing (misfiled notes, ambiguous rules), it proposes a taxonomy patch as a normal `update_note` — visible in git, revertible, never silent deviation. The schema is expected to be the most-edited file in a young wiki.
- Tool descriptions are prompts and get iterated like prompts: horde's audit log (§10) shows how tools are actually (mis)used — wrong modes chosen, failed patches, paths guessed. Review it periodically and refine descriptions; ship as config-reloadable text (`descriptions.yaml` overriding built-ins) so iteration doesn't require a rebuild.
- Git history doubles as the improvement metric: commits-per-wiki-per-week, patch:replace ratio, and lint findings over time indicate whether the system is getting healthier. A `horde stats` subcommand (v1.5) can compute these from `git log` greps.

## 14. Milestones

- ✅ **M1 — Read path:** scaffold with official SDK, Streamable HTTP, bearer middleware, `search_notes` / `list_notes` / `read_note`, path-safety tests. Verify with MCP Inspector locally.
- ✅ **M2 — Write path + git:** `create/update/delete/move`, atomic writes, auto-commit, audit logging.
- ✅ **M3 — Exposure (done 2026-07-12):** docker compose + cloudflared tunnel, OAuth 2.1 connector auth (replaced the WAF-header plan, §4), custom connector live on claude.ai, `hello` tool as a vault-free connectivity check. Outstanding from the original scope: WAF rule (now optional hardening, must be `/mcp`-scoped) and the Claude-mobile end-to-end pass.
- ✅ **M4 — Polish (done 2026-07-12):** `read_note` offset/length paging, conflict-marker detection on read and write, `history`/`restore` tools, async push via `HORDE_GIT_PUSH`.
- ✅ **M5 / v1.5 — Graph + attachments (done 2026-07-12):** `link_graph` for lint at scale; read-only attachment serving via MCP resources (`wiki://`) and `list_attachments` / `read_attachment` — allowlisted types, correct MIME, size cap, path safety under `HORDE_ROOT`; no binary writes; no HTTP static file endpoint. Config: `HORDE_MAX_ATTACHMENT_BYTES`, `HORDE_ATTACHMENT_EXTS`.
- ✅ **M6 / v1.6 — Render Links (done 2026-07-13):** `url` field on `list_attachments` / `read_attachment` (when `HORDE_PUBLIC_URL` is set) + the signed `GET /render/{path}` route (ADR-10). Same allowlist / cap / path safety as `read_attachment`; boot-generated HMAC secret; ~6 h expiry; `Cache-Control: private, no-store`. Outstanding: the claude.ai inline-display pass needs the live deploy to verify.
- ✅ **M7 / v1.7 — Uploads, video, downloads (done 2026-09-13):** `create_attachment` (base64) + the bearer-gated streaming `PUT|POST /attachments/{path...}` route, plus `delete_attachment` / `move_attachment` (ADR-11). Video in the allowlist, `http.ServeContent` streaming with Range, and `download_url` / `?download=1` for save-to-disk (ADR-12). Supersedes §7's "no binary writes"; same allowlist / path safety as reads, one git commit per write, all absent under `HORDE_READONLY`. Config: `HORDE_MAX_STREAM_BYTES`.
- Deferred **v2:** `update_links_on_move`, `daily_note`, vault-wide `replace_all` (see §7).

## 15. Acceptance Criteria

- From Claude mobile: "find my notes about OPNsense and add a section on the Deco LED fix" completes using `search_notes` → `read_note` → `update_note(patch)`.
- Any mutation appears as a discrete git commit with tool name and path.
- A request without the bearer token is blocked at Cloudflare and (if it somehow arrives) rejected by the server.
- `go test ./...` covers path traversal, symlink escape, patch uniqueness, atomic write, and excluded-dir enforcement.

### v1.5 additions
- Claude (or MCP Inspector) can discover an allowlisted image/PDF under a wiki and fetch its bytes with the correct MIME, gated by the same auth as tools.
- Paths outside `HORDE_ROOT`, disallowed extensions, excluded dirs, and oversize files are rejected. (v1.7 lifts the "binary write tools do not exist" half of this; the rejections still apply, now on writes too — ADR-11.)
- `list_notes` / `search_notes` still skip non-`.md`; attachment discovery does not require changing note-tool semantics.
- No unauthenticated HTTP route serves vault binaries. (v1.6 narrows this to: no *unsigned* route — Render Links are the sole, signature-gated exception; see ADR-10.)

### v1.6 additions
- With `HORDE_PUBLIC_URL` set, `list_attachments` / `read_attachment` return a `url`; pasting it into a browser fetches the file with correct MIME until expiry.
- The same URL after expiry — or after a horde restart — returns an error, including through Cloudflare (no cached body served).
- A tampered path, expiry, or signature is rejected; disallowed extensions and excluded dirs are never signed and never served.
- An image embedded via its Render Link displays inline in a claude.ai conversation.

### v1.7 additions
- `create_attachment` with base64 content and `PUT /attachments/{path}` with a raw body both land the same bytes at the same path, produce one git commit each, and are immediately returned by `list_attachments`.
- An mp4 uploaded through the HTTP route downloads intact through its `download_url`, plays in a browser (Range requests answered with 206), and never comes back as bytes from `read_attachment`.
- Opening `url` displays an image in the browser; opening `download_url` saves it under its own filename.
- A file past `HORDE_MAX_STREAM_BYTES` is rejected on upload and leaves no partial file or temp file in the vault.
- Uploads of disallowed extensions, oversize bodies, paths outside a wiki, and traversal attempts are rejected by both transports; an upload without a valid bearer token writes nothing.
- Under `HORDE_READONLY`, the three attachment write tools are absent from `tools/list` and the upload route is not mounted.

## 16. Decision Log (ADRs)

### ADR-1 — Host & source of truth (2026-07-12)
**Decision:** horde runs on Justin's desktop workstation in a Docker container. The desktop's `~/Documents/Obsidian/` is the canonical source of truth. The wiki tree is not currently a git repo; it will be initialized as its own standalone repo (independent of any other repository), satisfying §6's startup precondition.
**Consequences:** container bind-mounts the vault read-write; git identity configured inside the container; cloudflared runs as a sidecar container on the same host. Availability is bounded by desktop uptime (see ADR-2).

### ADR-2 — Availability contract (2026-07-12)
**Decision:** Best-effort availability, bounded by desktop uptime. The desktop already runs 24/7; no dedicated always-on host for v1. Accepted failure modes: reboots, kernel updates, extended power events while away — Claude mobile gets a Cloudflare 530 when the tunnel is down.
**Revisit trigger:** if dead-tunnel moments become annoying in practice, migrate the container + vault replica to a low-power always-on box; the Docker packaging makes the move cheap by design.

### ADR-3 — File ownership & container UID (2026-07-12)
**Decision:** The container is the primary writer and owns the vault. Implementation: run the container with `user: 1000:1000` (Justin's host UID/GID) so "container owns it" and "Justin owns it" are the same UID — host-side Obsidian and rclone keep working without permission juggling. Git inside the container gets `safe.directory=/vault` and a fixed identity (`horde <mcp@pluto>`).

### ADR-4 — Git staging invariant & human edits (2026-07-12)
**Decision:** (1) Hard invariant: horde stages ONLY the explicit file paths it just wrote — never directories, never `git add -A` on a tree. Enforced in code and covered by tests (a planted "dirty bystander" file must never appear in a horde commit). (2) Human edits via Obsidian are ignored by horde's git layer: they remain permanently uncommitted, visible as working-tree drift. Git history is Claude-only by design; human work is protected by the rclone→iDrive backup, not by git.
**Consequences:** `git status` on the vault will always look dirty; this is normal. Reverting any horde commit is safe with respect to human work. Startup precondition in §6 relaxes accordingly: dirty working tree is expected, not an error.

### ADR-5 — Concurrency via git isolation, not locks (2026-07-12)
**Decision:** Scheduled/batch jobs (lint, future automation) run in their own container and never touch the live working tree. Topology: a bare hub repo (`horde.git`) on the same host; the live vault pushes after every horde commit; batch containers `clone → edit → commit → push`, and horde integrates by pulling into the working tree.
**Why this wins:** overlapping edits become explicit git merge conflicts instead of silent last-write-wins clobbers — a conflict is a surfaced problem, an overwrite is an invisible one. Pull into a dirty tree is safe: git refuses if incoming changes touch files with uncommitted human edits.
**Residual risk (accepted for v1):** two *interactive* sessions hitting the live tree simultaneously can still last-write-wins each other. Single user, low odds; compare-and-swap (`expected_hash` on mutating tools) remains the documented v2 upgrade if it ever bites.

### ADR-6 — Secrets & sensitive data policy (2026-07-12)
**Decision:** Hard rule: no secrets, credentials, PII, or otherwise sensitive data in the wiki, ever. Secrets live in a local secrets manager on the host; the wiki may reference a secret by name/location ("router admin creds: in secrets manager under `network/opnsense`") but never by value. Horde's own bearer token and any service credentials are injected into containers from the secrets manager, not stored in the vault or compose files.
**Enforcement:** (1) taxonomy.md files state the rule so agents refuse to write secret-shaped content and flag it if asked; (2) the lint pass greps for credential patterns (key-shaped strings, `password:`, private key headers, etc.) and reports hits; (3) pre-existing history containing a leaked secret is treated as an incident: rotate the secret, then scrub history.
**Open (carried to next question):** per-wiki access levels and token rotation cadence were not yet decided.

### ADR-7 — Work content excluded (2026-07-12)
**Decision:** Horde is for personal knowledge only. No Jack Henry / work content enters `~/Documents/Obsidian/` — option (a), enforced by policy rather than code. No `work/` wiki exists; per-wiki ACLs deferred until a real second-tier need appears.
**Consequences:** one token / full-CRUD-everywhere stays acceptable because everything behind it is personal. Work notes live in work-sanctioned tooling, out of scope.

### ADR-8 — Single-device editing; sync is git + iDrive only (2026-07-12)
**Decision:** The vault is edited on the desktop only (Obsidian) plus horde. No Syncthing, no Obsidian Sync, no phone/laptop replicas. Replication is exactly two mechanisms: the git hub repo (versioning + batch-job isolation per ADR-5) and rclone→iDrive (disaster recovery). Mobile access to the wiki IS horde — Claude is the phone client.
**Consequences:** all Syncthing considerations removed from the spec (excluded dirs, conflict files, sync-race risks). The concurrency surface shrinks to: Obsidian-vs-horde on the desktop, and interactive-vs-interactive horde sessions (ADR-5 residual).

### ADR-9 — In-process OAuth 2.1 for connector auth (2026-07-12)
**Context:** claude.ai custom connectors turned out to require the OAuth handshake — the request-header option from §4's original plan was not offered (risk §12.1 materialized).
**Decision:** Implement the minimal single-user OAuth 2.1 flow inside horde (`internal/oauth`) rather than fronting it with a proxy: discovery metadata (RFC 9728 + 8414), dynamic client registration, authorization code + PKCE (S256). The "login" is pasting `HORDE_TOKEN` into horde's authorize page, and the issued access token **is** `HORDE_TOKEN` — the bearer middleware is untouched, tokens survive restarts, and there is exactly one credential in the system.
**Consequences:** in-memory client registrations are lost on restart; mitigated by allowlisting claude.ai/claude.com callback hosts for unknown client IDs (safe: an attacker-initiated authorize still requires the token and can only redirect to claude.ai). Any future WAF rule must exempt `/oauth/*` and `/.well-known/*`. Token rotation = rotate `HORDE_TOKEN` and reconnect the connector once.

### ADR-10 — Render Links: one signed HTTP route for attachments (2026-07-13)
**Context:** claude.ai does not render `ImageContent` from connector tool results, so v1.5's MCP-only attachment fetch leaves image-bearing notes invisible in chat.
**Decision:** Supersede §7's "No HTTP static files" rule with exactly one route: attachment tools mint HMAC-signed, ~6 h-expiring **Render Links** (`GET /render/{path}?exp&sig`) serving precisely what `read_attachment` serves. A render helper for MCP conversations, not a file server. Full record: [docs/adr/0010-render-links-for-attachments.md](docs/adr/0010-render-links-for-attachments.md); glossary: [CONTEXT.md](CONTEXT.md).
**Consequences:** boot-generated secret (restart = mass revocation, zero secret management); `Cache-Control: no-store` required because of Cloudflare edge caching; if claude.ai ever renders `ImageContent`, the route can be retired.

### ADR-11 — Attachment uploads: two write paths, one rule set (2026-09-13)
**Context:** v1.5 made attachments read-only on the assumption that binaries only arrive from Obsidian or a human; agents also produce files that belong next to notes, and filing each one meant leaving the session.
**Decision:** Allow binary writes for exactly the files horde already agrees to read, via `create_attachment` (base64, in-session) and a bearer-gated `PUT|POST /attachments/{path...}` (raw body), both funnelling through one write path with the existing allowlist, size cap, path safety, and git commit; `delete_attachment` / `move_attachment` complete parity with the note tools. Supersedes §7's "No binary writes". Full record: [docs/adr/0011-attachment-uploads.md](docs/adr/0011-attachment-uploads.md); glossary: [CONTEXT.md](CONTEXT.md).
**Consequences:** binaries now enter git history (blobs don't delta-compress, so `.git/` grows faster; the size cap bounds each commit and rclone already backs up `.git/`); the extension allowlist becomes a write-side gate as much as a read-side one; uploads are indistinguishable from human-filed files except by commit message.

### ADR-12 — Streaming downloads: video in the allowlist, save as well as view (2026-09-13)
**Context:** ADR-10's route buffered whole files, served them with no `Content-Disposition`, and the allowlist stopped at PDF — so vault video could be neither stored nor served, and "view this" had no "keep this".
**Decision:** Allowlist `mp4`/`m4v`/`webm`/`mov`; stream the route through `http.ServeContent` (Range support for video seeking and resumable downloads); split the caps by mechanism (`HORDE_MAX_ATTACHMENT_BYTES` for in-memory/base64 paths, `HORDE_MAX_STREAM_BYTES` for streaming HTTP ones); return `download_url` (`?download=1` → `Content-Disposition: attachment`) alongside `url` on every attachment tool; and have `read_attachment` stat first, returning links with `inlined: false` for video and oversize files. Full record: [docs/adr/0012-streaming-downloads-and-video.md](docs/adr/0012-streaming-downloads-and-video.md).
**Consequences:** `.git/` grows faster with video blobs; downloads cost file descriptors and bandwidth rather than RAM; a leaked link now exposes a potentially much larger file for the same ~6 h; no transcoding or thumbnailing (horde serves what is in the vault).

### ADR-13 — Binaries out of git; iCloud is their backup (2026-10-12)
See `docs/adr/0013-binaries-out-of-git.md`. Supersedes the git parts of ADR-11/12.
