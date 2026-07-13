# horde

An Obsidian vault served as a remote MCP server, so Claude (claude.ai web and mobile) can read, search, create, edit, and delete notes — with every mutation landing as a discrete git commit. Design and decision log: [horde-spec.md](horde-spec.md).

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
| `HORDE_READONLY` | `false` | Disable all mutating tools |

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
