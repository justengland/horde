# Horde

Horde exposes a single Obsidian vault to AI agents over MCP — notes are editable, everything else is read-only.

## Language

**Wiki**:
A top-level folder of the vault. Every path horde accepts begins with a wiki name.

**Note**:
A markdown document inside a wiki; the only kind of file horde will create, edit, move, or delete.
_Avoid_: page, document, file

**Attachment**:
An allowlisted binary file — image, PDF, or video — inside a wiki. Readable and, since v1.7 (ADR-11), writable — `create_attachment` or the bearer-gated upload route puts one in the vault under the same allowlist, size cap, and path rules that govern reads.
_Avoid_: file, asset, media, static file

**Upload**:
The act of putting an Attachment into the vault, by either transport: the `create_attachment` MCP tool (base64) or `PUT|POST /attachments/{path}` (raw body, bearer-gated). Not a noun for the file itself — that is an Attachment.
_Avoid_: attach, post, push, sync

**Render Link**:
A short-lived signed URL for one Attachment, minted only inside MCP responses so a chat client can display the attachment inline. It expires within hours and is not a way to share vault content with people or systems outside an MCP session.
_Avoid_: permalink, share link, public URL

**Download Link**:
The same Render Link with `?download=1`, which makes the browser save the file under its own name instead of rendering it. Same signature, same expiry, same one Attachment — only the `Content-Disposition` differs. Tools return it as `download_url`.
_Avoid_: export link, share link, permalink
