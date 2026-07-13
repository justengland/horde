# Horde

Horde exposes a single Obsidian vault to AI agents over MCP — notes are editable, everything else is read-only.

## Language

**Wiki**:
A top-level folder of the vault. Every path horde accepts begins with a wiki name.

**Note**:
A markdown document inside a wiki; the only kind of file horde will create, edit, move, or delete.
_Avoid_: page, document, file

**Attachment**:
An allowlisted binary file (image or PDF) inside a wiki. Readable through horde, never writable.
_Avoid_: file, asset, media, upload, static file

**Render Link**:
A short-lived signed URL for one Attachment, minted only inside MCP responses so a chat client can display the attachment inline. It expires within hours and is not a way to share vault content with people or systems outside an MCP session.
_Avoid_: permalink, share link, public URL, download link
