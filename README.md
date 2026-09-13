# horde

An Obsidian vault served as a remote MCP server, so Claude (claude.ai web and mobile) can read, search, create, edit, and delete notes — and file images, PDFs, and video alongside them — with every mutation landing as a discrete git commit. Design and decision log: [horde-spec.md](horde-spec.md).

```
Claude ── HTTPS/OAuth ──> Cloudflare edge ── tunnel ──> horde (Docker) ──> ~/Documents/Obsidian (git)
```

## Run it

Secrets live in a dotenvx-encrypted `.env` (`HORDE_TOKEN`, `TUNNEL_TOKEN`); the decryption key is in `.env.keys`, which is never committed. All compose invocations go through `dotenvx run --`, wrapped in make targets:

```bash
make up          # build + start horde only (local testing; http://127.0.0.1:8787)
make up-tunnel   # start horde + cloudflared sidecar (publicly reachable)
make hello       # end-to-end smoke test: healthz → MCP session → create/read/delete a note
make logs        # follow container logs
make down        # stop everything
```

`make hello` is the fastest "is it working" check — it exercises the full write path including the git auto-commit, and cleans up after itself.

### Configuration (env)

| Var | Default | Purpose |
|---|---|---|
| `HORDE_TOKEN` | *(required)* | Bearer token; also what you paste into the OAuth approval page |
| `HORDE_ROOT` | `~/Documents/Obsidian` | Vault path (mounted at `/vault` in compose) |
| `HORDE_PUBLIC_URL` | set in `compose.yml` | External base URL; enables the OAuth endpoints |
| `HORDE_GIT` / `HORDE_GIT_PUSH` | `on` / `false` | Git auto-commit / async push after commit |
| `HORDE_READONLY` | `false` | Disable all mutating tools, including attachment uploads |
| `HORDE_MAX_ATTACHMENT_BYTES` | `5242880` | Cap for in-memory paths: MCP inline reads and base64 uploads |
| `HORDE_MAX_STREAM_BYTES` | `536870912` | Cap for streaming paths: the download route and HTTP uploads |
| `HORDE_ATTACHMENT_EXTS` | `png,jpg,jpeg,gif,webp,pdf,mp4,webm,mov,m4v` | Allowlisted attachment extensions (gates reads and uploads) |

## Attachments: upload and download

Images, PDFs, and video (the `HORDE_ATTACHMENT_EXTS` allowlist) can be written into the vault two ways. In an MCP session, Claude calls `create_attachment` with base64 bytes. From a terminal, PUT the raw file — better for anything past a few hundred KB, since base64 costs ~33% in tokens:

```bash
curl -X PUT \
  -H "Authorization: Bearer $(dotenvx get HORDE_TOKEN)" \
  --data-binary @rack.png \
  "https://horde.crucibleplatform.com/attachments/Pegasus/90%20Assets/Photos/rack.png"
```

Add `?overwrite=true` to replace an existing file. Both paths apply the same rules: allowlisted extension, inside a wiki, one git commit per write, and nothing at all when `HORDE_READONLY` is set. The two caps differ by mechanism — `create_attachment` holds the file in memory and is capped at `HORDE_MAX_ATTACHMENT_BYTES` (5 MB), while the HTTP route streams to disk and is capped at `HORDE_MAX_STREAM_BYTES` (512 MB), which is what makes video uploadable.

Every attachment tool returns two short-lived (~6 h) links: `url` displays the file inline in chat or a browser, and `download_url` (the same link plus `?download=1`) saves it under its own filename. Downloads stream and answer Range requests, so a browser can seek through a video and an interrupted download resumes. Video is never returned as bytes in an MCP response — `read_attachment` answers with `inlined: false` and the links instead. `delete_attachment` and `move_attachment` mirror the note tools — git-recoverable, and like `move_note` they don't rewrite the `![[embeds]]` pointing at the file. See [ADR-11](docs/adr/0011-attachment-uploads.md) and [ADR-12](docs/adr/0012-streaming-downloads-and-video.md).

## Token generation

```bash
openssl rand -base64 32 | tr '+/' '-_' | tr -d '='   # 32 bytes, base64url
dotenvx set HORDE_TOKEN "<value>"                     # encrypts into .env
make up                                               # restart to pick it up
```

Rotating the token invalidates Claude's stored access token; reconnect the connector once afterwards.

## Connecting claude.ai

1. Make sure the tunnel is up (`make up-tunnel`) and `https://horde.crucibleplatform.com/healthz` returns JSON.
2. claude.ai → Settings → Connectors → **Add custom connector**.
3. URL: `https://horde.crucibleplatform.com/mcp`. Leave OAuth Client ID/Secret **blank** — Claude registers itself dynamically.
4. Click Connect: horde's approval page opens. Paste the token (`dotenvx get HORDE_TOKEN`) and approve.
5. Sanity check: ask Claude to *"call the hello tool on horde"* — it responds without touching the vault.

The connector syncs to Claude mobile automatically. Cloudflare side: the tunnel's public hostname must point at service `HTTP://horde:8787` (the compose service name — TLS terminates at the edge, and the tunnel itself is encrypted).

## Restore recipes

Every horde mutation is one commit (`mcp(<tool>): <path>`), authored by `horde <mcp@pluto>`. Human Obsidian edits are intentionally never committed (spec ADR-4), so reverting horde commits never touches your own work. In the vault:

```bash
git log --oneline --author=horde              # what has Claude done
git show <hash>                               # inspect one change
git checkout <hash>^ -- <path>                # restore a file as it was before <hash>
git revert <hash>                             # undo one mutation as a new commit
```

`delete_note` responses include the exact restore command as `restore_hint`. From Claude, `history` lists commits for a path and `restore` checks one out as a new commit.

## Development

```bash
make hooks    # one-time: install pre-commit hook (gofmt + vet + test)
make check    # go vet + go test ./...
```

Layout: `cmd/horde` (entrypoint) · `internal/server` (HTTP + MCP tools) · `internal/vault` (store, atomic writes) · `internal/git` (commit layer) · `internal/oauth` (single-user OAuth 2.1 for claude.ai) · `internal/auth` (bearer middleware) · `internal/paths` (path safety).
