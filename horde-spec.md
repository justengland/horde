# horde — Obsidian Vault MCP Server (Go)

**Status:** Draft v1 · 2026-07-12
**Owner:** Justin
**Goal:** Expose an Obsidian vault (plain markdown directory) as a remote MCP server so Claude.ai / Claude mobile can read, search, create, edit, and delete notes, with git as the safety net.

---

## 1. Overview

A standalone Go binary that serves the Model Context Protocol over **Streamable HTTP**, operating directly on the vault filesystem. The vault lives on Justin's desktop (24/7), which is the sole source of truth; the only write paths are Obsidian on that desktop and horde itself. The server is published to the internet via a **Cloudflare Tunnel**; auth is enforced at both the Cloudflare edge and in the server itself. Every mutation is committed to git automatically, so any change Claude makes is inspectable and revertible.

```
Claude (Anthropic cloud)
   │  HTTPS  (Authorization: Bearer <token>)
   ▼
Cloudflare edge  ── WAF rule: reject requests without valid header ──┐
   │  cloudflared tunnel (outbound-only from lab)                    │
   ▼                                                                 │
horde (Go, 127.0.0.1:8787)  ── validates bearer token ◄──────────┘
   │  fs read/write (atomic)
   ▼
~/Documents/Obsidian  (git repo; sole copy — replicated only via git hub + rclone)
```

Non-goals for v1: multi-vault support, multi-user auth, wikilink graph analysis, Obsidian plugin/UI features (canvas rendering, dataview execution).

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
- `GET /healthz` — liveness: returns vault path readable, git repo OK, version.
- Binds `127.0.0.1:8787` only. Nothing listens on LAN/WAN; cloudflared connects over loopback.
- Session handling per SDK defaults; server is effectively stateless between calls (no in-memory vault index in v1).

## 4. Authentication

Two independent layers; both must pass.

**Layer 1 — Cloudflare edge.** A WAF custom rule on the tunnel hostname: block any request where `http.request.headers["authorization"][0]` does not equal `Bearer <token>` (or, minimally, where the header is absent). This drops internet noise before it ever reaches the lab.

> **Why not Cloudflare Access service tokens:** Claude's custom-connector request headers are restricted to an allowlist (`authorization`, `x-api-key`, `x-auth-token`). CF Access service tokens require `CF-Access-Client-Id` / `CF-Access-Client-Secret` headers, which Claude cannot send. The WAF-rule approach gets equivalent edge filtering using the `authorization` header Claude *can* send.

**Layer 2 — horde.** Middleware validates `Authorization: Bearer <token>` with constant-time compare against `HORDE_TOKEN`. 401 otherwise. Token: 32+ bytes random, base64url.

**Connector wiring.** Add as a custom connector on claude.ai web (syncs to mobile). Use the Request headers (beta) option: header `authorization`, value `Bearer <token>`. Fallback if the header beta isn't enabled on the account: implement the minimal OAuth 2.1 flow the SDK supports, or front the server with an OAuth-terminating proxy. Treat this as a known risk (§12).

## 5. Data Model — Wikis

- `HORDE_ROOT` = `~/Documents/Obsidian/` (absolute path, a git repo).
- **Every top-level folder under the root is an independent wiki** (e.g. `network/`, `pegasus/`, `recipes/`). Horde treats them as namespaces; there is no cross-wiki implicit behavior.
- **Each wiki carries a `taxonomy.md` at its root** describing that wiki's structure, naming, frontmatter, and editing conventions. This file is the contract for how agents edit that wiki. Wikis follow the llm-wiki pattern (Karpathy): per-wiki `index.md` (routing catalog, read before querying), `log.md` (append-only, greppable `## [date] op | title` entries), optional immutable `raw/` sources, and periodic lint passes — all defined in the taxonomy, not in horde. Horde stays a dumb, safe file server; wiki discipline lives in the schema files it serves.
- **Taxonomy enforcement flow:** the server's MCP `instructions` (initialize response) state: *"Before creating or editing notes in a wiki, read `<wiki>/taxonomy.md` and follow it. If it does not exist, propose one using `create_note` before making other changes."* Lazy-read keeps the instructions payload small regardless of wiki count.
- A `list_wikis` tool (see §7) enumerates top-level wikis with note counts and whether a `taxonomy.md` exists, so Claude can orient in one call.
- **Writable extensions:** `.md` only in v1 (`.canvas` read-only).
- **Excluded from all operations:** `.obsidian/`, `.git/`, `.trash/`, loose files at the root itself (notes must live inside a wiki), plus configurable ignore globs.
- Paths in the API are always root-relative with forward slashes and must begin with a wiki name (`network/opnsense/wan-failover.md`).
- Frontmatter is parsed when present and returned as structured data alongside body content.

### Path safety (hard requirements)
1. Clean and resolve every input path; reject anything escaping `HORDE_ROOT` after `filepath.EvalSymlinks` (defeats `..` and symlink tricks).
2. Reject absolute paths, empty paths, paths into excluded dirs, and disallowed extensions on write.
3. Max path length 255 bytes/segment; UTF-8 validated.

### Write safety
- Atomic writes: write to `.<name>.tmp-<rand>` in the same directory, fsync, rename. No torn files even if the box loses power or a backup job reads mid-write.
- Max note size 2 MiB (configurable); reject larger writes.
- `update_note` patch mode requires `old_str` to be unique in the file (str-replace semantics) — fails loudly rather than guessing.

## 6. Git Safety Net

- Precondition at startup: `HORDE_ROOT` is a git repo with a clean-enough state (untracked files OK). Refuse to start otherwise unless `HORDE_GIT=off`.
- **Every mutating tool call** runs, after a successful write:
  `git add -A <path> && git commit -m "mcp(<tool>): <path>" --author "horde <mcp@pluto>"`
- Deletes are **soft**: `delete_note` does `git rm` + commit (recoverable via git), never `rm` of untracked content without a prior `git add`.
- Optional `HORDE_GIT_PUSH=1`: async `git push` after commit, best-effort with backoff; failures logged, never block the tool response.
- v1.5 candidates: `history(path)` and `restore(path, ref)` tools.

### Backup strategy (git vs rclone)
Git and the existing rclone→iDrive job solve different problems and both stay:
- **Git** = versioning and blame for horde's edits. Local commits are the primary safety net; `HORDE_GIT_PUSH` to a private remote is optional convenience, not the backup.
- **rclone→iDrive** = disaster recovery for the whole vault: attachments, `.obsidian/` config, and the `.git/` directory itself (which carries the full history offsite for free). Keep the existing schedule; ensure the job includes `.git/` and excludes nothing horde writes.
One caveat: don't run `rclone sync` mid-write; horde's atomic rename makes torn files unlikely, but scheduling the job off-hours (or using `--backup-dir` versioning on iDrive) keeps restores clean.

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
- **In:** `path` (required), `include_frontmatter` (bool, default true)
- **Out:** `{path, frontmatter (object|null), body, size, modified}`. Files >100 KiB returned with a truncation flag and instructions to use `read_note` with `offset/length` (v1.5) — v1 just truncates head 100 KiB.

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

### `link_graph` (v1.5 — required for lint at scale)
Parse `[[wikilinks]]` across one wiki and return the adjacency map without the agent reading every page body.
- **In:** `wiki` (required)
- **Out:** `{pages: [{path, out_links, in_link_count}], orphans: [path], broken_links: [{path, target}]}`
- Powers the lint pass's orphan and broken-link checks in one call instead of N reads.

### Deferred (v2)
`update_links_on_move`, `daily_note`, `history`/`restore`, resources (`wiki://` URIs), vault-wide `replace_all`.

## 8. Tool Descriptions (prompt surface)

Descriptions ship in the binary and matter as much as code. Requirements: state path conventions (root-relative, must start with a wiki name), state that `patch` is preferred over `replace`, state that deletes are git-recoverable, and state that `move_note` doesn't fix links. Include one example call per tool in the description.

## 9. Configuration

| Env | Default | Purpose |
|---|---|---|
| `HORDE_ROOT` | `~/Documents/Obsidian` | Root containing wiki folders |
| `HORDE_TOKEN` | — (required) | Bearer token |
| `HORDE_ADDR` | `127.0.0.1:8787` | Listen address |
| `HORDE_READONLY` | `0` | Disable all mutating tools |
| `HORDE_GIT` | `on` | `on|off` |
| `HORDE_GIT_PUSH` | `0` | Async push after commit |
| `HORDE_IGNORE` | — | Extra ignore globs, comma-sep |
| `HORDE_MAX_NOTE_BYTES` | `2097152` | Write size cap |

## 10. Observability

- `slog` JSON to stdout (journald). One line per tool call: tool, path(s), duration, bytes, result, commit hash, remote session id. No note *content* in logs.
- `/healthz` for uptime-kuma or similar.
- Later: OTLP/Datadog exporter behind a build tag.

## 11. Deployment

- `CGO_ENABLED=0 go build` → single binary; systemd unit with `EnvironmentFile=/etc/horde.env`, `ProtectSystem=strict`, `ReadWritePaths=<vault>`, `DynamicUser=no` (needs git identity).
- `cloudflared` config: one ingress rule `obsidian.<domain>` → `http://127.0.0.1:8787`, catch-all 404.
- Cloudflare WAF custom rule per §4.
- README covers: token generation, connector setup on claude.ai web, restore-from-git recipes.

## 12. Risks & Open Questions

1. **Request-header beta availability.** If the account can't use header auth for custom connectors, v1 needs the OAuth fallback (SDK auth support or proxy). Verify early — this is the only external dependency that could force design change.
2. **Concurrent interactive edits** (Obsidian save racing a horde write on the same file): atomic rename limits damage to last-write-wins on whole-file content; accepted for v1 per ADR-5's residual-risk note.
4. **Rate/abuse:** single-user, token-gated; no rate limiting in v1 beyond Cloudflare defaults.

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

- **M1 — Read path:** scaffold with official SDK, Streamable HTTP, bearer middleware, `search_notes` / `list_notes` / `read_note`, path-safety tests. Verify with MCP Inspector locally.
- **M2 — Write path + git:** `create/update/delete/move`, atomic writes, auto-commit, audit logging.
- **M3 — Exposure:** cloudflared tunnel, WAF rule, custom connector on claude.ai, end-to-end test from Claude mobile.
- **M4 — Polish:** truncation/offsets, conflict-file handling, history/restore, optional push.

## 15. Acceptance Criteria

- From Claude mobile: "find my notes about OPNsense and add a section on the Deco LED fix" completes using `search_notes` → `read_note` → `update_note(patch)`.
- Any mutation appears as a discrete git commit with tool name and path.
- A request without the bearer token is blocked at Cloudflare and (if it somehow arrives) rejected by the server.
- `go test ./...` covers path traversal, symlink escape, patch uniqueness, atomic write, and excluded-dir enforcement.

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
