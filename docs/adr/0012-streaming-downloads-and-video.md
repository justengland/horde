---
status: accepted
---

# ADR-12 — Streaming downloads: video in the allowlist, save as well as view

ADR-10 built the render route to do one thing: put an image in front of a chat client. It read the whole file into memory, served it with a bare `Content-Type`, and the allowlist held images and PDFs only. Two gaps followed from that shape. A vault holds video — a clip of a failing fan, a screen recording of a config walkthrough — and horde could neither store nor serve it. And "look at this" is not "keep this": a browser handed `image/png` with no `Content-Disposition` renders it, and saving means right-click-and-hope, which is worse on mobile and impossible for a `video/mp4` the browser decides to stream instead.

We decided:

- **Video joins the allowlist** — `mp4`, `m4v`, `webm`, `mov`, in the defaults alongside images and PDF.
- **The route streams** via `http.ServeContent` instead of buffering. That brings Range support with it, which is not a nicety: a browser's `<video>` element seeks with Range, and a download interrupted at 300 MB resumes with it.
- **Two caps, split by mechanism, not by file type.** `HORDE_MAX_ATTACHMENT_BYTES` (5 MB) governs what may be held in memory and base64'd into an MCP response. `HORDE_MAX_STREAM_BYTES` (512 MB) governs the HTTP paths that stream — the download route and the upload route, which now writes to a temp file as bytes arrive rather than buffering the request.
- **Every attachment tool returns `url` and `download_url`.** Same signed link; `download_url` carries `?download=1`, which flips `Content-Disposition` from `inline` to `attachment` and names the file. The signature covers path and expiry only, so the parameter grants nothing a plain Render Link doesn't.
- **`read_attachment` stats before it reads.** Video, and anything past the inline cap, returns metadata and links with `inlined: false` instead of failing or trying to base64 a 200 MB clip into a JSON-RPC response.

## Considered Options

- **Always `Content-Disposition: attachment`** — rejected: it breaks ADR-10's whole reason for existing, since an `<img src>` pointing at a forced download displays nothing.
- **A separate `/download/{path}` route** — rejected: same bytes, same signature, same handler, differing by one header. A query parameter says that more honestly than a second route.
- **One cap for everything, raised to 512 MB** — rejected: the cap on `read_attachment` exists because the bytes land in an MCP response as base64. Raising it to suit video would let a 200 MB file try to become a JSON-RPC message.
- **Transcode or thumbnail video** — rejected: horde serves what is in the vault. ffmpeg is a dependency, a CPU budget, and a decision about quality that belongs to whoever made the file.

## Consequences

- Vault `.git/` grows faster still: video blobs are large and don't delta-compress. `HORDE_MAX_STREAM_BYTES` is the knob that bounds how fast, and it is worth lowering on a small disk.
- The render route no longer holds a whole file in memory, so concurrent large downloads cost file descriptors and bandwidth rather than RAM.
- `Cache-Control: private, no-store` still applies to ranged responses, so Cloudflare cannot assemble a cached copy of a video from range requests.
- A download link is exactly as leakable as a Render Link, and now names a file that may be a few hundred megabytes. The six-hour expiry and boot-generated secret are unchanged; the exposure per leak is larger.
- Video in the allowlist means video can be *uploaded* too — the allowlist gates both directions (ADR-11).
