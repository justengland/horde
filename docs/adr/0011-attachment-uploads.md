---
status: accepted
---

# ADR-11 — Attachment uploads: two write paths, one rule set

The spec (§7, v1.5) made attachments read-only — "No binary writes" — on the reasoning that binaries are Obsidian/human-side and rclone-backed, so horde only needed a fetch path. That holds right up until an agent produces or receives a file that belongs in the vault: a diagram it generated, a screenshot the human handed it, a PDF worth filing next to the note that cites it. Today the only way in is to leave the MCP session, which makes "put this with the note" a manual chore and leaves notes referencing files that aren't there.

We decided to allow binary writes for exactly the files horde already agrees to read, through two transports:

- **`create_attachment` MCP tool** — takes `content_base64`. In-session, works from any MCP client, but base64 costs ~33% overhead in tokens, so it suits small files.
- **`PUT|POST /attachments/{path...}`** — bearer-gated, raw body. The efficient path for real files from a terminal or script, and the only sane one for multi-megabyte PDFs.

Both funnel into one `Store.WriteAttachment`, so there is a single set of rules: the configured extension allowlist (`HORDE_ATTACHMENT_EXTS`), the size cap (`HORDE_MAX_ATTACHMENT_BYTES`), the same path safety as reads (inside a wiki, no traversal, no excluded dirs), a git commit per write, and refusal when `HORDE_READONLY` is set. `delete_attachment` and `move_attachment` round out parity with the note tools — git-recoverable, `confirm`-gated, and (like `move_note`) they do not rewrite the `![[embeds]]` pointing at the file.

The upload route is bearer-gated, never signed-and-public. Render Links (ADR-10) are deliberately unauthenticated so an `<img src>` in a chat client can fetch them; that trade-off is defensible for reading one attachment for six hours and indefensible for writing into the vault.

## Considered Options

- **Keep attachments read-only** — rejected: it is the status quo that sends the human out of the session to move a file, and leaves agent-authored notes pointing at files nobody filed.
- **MCP tool only** — rejected: base64 in a tool argument makes a 4 MB PDF absurd, and horde already runs an HTTP server.
- **HTTP route only** — rejected: an agent inside claude.ai has no way to call it, so the in-session case stays unsolved.
- **Signed upload links, mirroring Render Links** — rejected: a leaked read link exposes one file for six hours; a leaked write link lets a stranger put bytes in the vault. Writes get the bearer gate.
- **An `HORDE_ATTACHMENT_WRITE` opt-in flag** — rejected: horde already has one write switch (`HORDE_READONLY`), and a second knob for the same question is a knob to forget.

## Consequences

- Binary writes are now part of horde's surface; the spec's §7 "No binary writes" rule and the v1 non-goal are superseded, and CONTEXT.md's Attachment definition is no longer "never writable".
- Vault git history now carries binaries. Blobs don't delta-compress, so the repo grows faster than a notes-only vault — the size cap bounds each commit, but a heavy upload habit will show up in `.git/`. The rclone→iDrive job already covers `.git/`, so recovery is unchanged.
- Uploaded files are immediately visible to `list_attachments` / `read_attachment` / Render Links, because they land in the same tree the read surface walks. Nothing special marks them as agent-authored except the commit message.
- The allowlist is now a write-side gate as much as a read-side one: widening `HORDE_ATTACHMENT_EXTS` widens what can be *put into* the vault, not just what can be read out of it.
