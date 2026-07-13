---
status: accepted
---

# ADR-10 — Render Links: one signed HTTP route for attachments

The spec (§7, v1.5) deliberately ruled out HTTP static-file serving — attachments were MCP-only to keep the whole read surface behind the bearer/OAuth gate. That held until we hit a client limitation: `read_attachment` already returns MCP `ImageContent` blocks, but claude.ai does not render image blocks from connector tool results, so image-bearing notes stay invisible in chat. We decided to add exactly one HTTP route that serves a **Render Link**: an HMAC-signed, ~6-hour-expiring URL that `list_attachments` / `read_attachment` include (as a `url` field, only when `HORDE_PUBLIC_URL` is set). The route serves precisely what `read_attachment` serves — same extension allowlist, same size cap, same path rules — nothing more. This is a render helper for MCP conversations, not a file server: links live only in chat responses, and stale images in old conversations are acceptable.

## Considered Options

- **Fully public static files** — rejected: the "accidental public object storage" the spec warned about.
- **Bearer-gated HTTP route** — rejected: `<img src>` in a chat UI cannot send an Authorization header, so it wouldn't solve the driver.
- **Wait for claude.ai to render ImageContent** — rejected: timeline outside our control. If claude.ai ships it, this route loses its reason to exist and can be retired.

## Consequences

- New public-but-signed surface. Mitigations: signature covers path + expiry; the signing secret is generated at boot, so a restart revokes every outstanding link (acceptable for a render helper, and it means no secret to manage or rotate).
- The route must send `Cache-Control: private, no-store` (or a max-age bounded by the link's expiry) — horde sits behind Cloudflare, and edge caching would otherwise keep serving an expired link.
- Spec §3 ("No public static-file / attachment HTTP routes") and §7 ("No HTTP static files") are superseded; the spec records this as ADR-10 / milestone M6 (v1.6).
