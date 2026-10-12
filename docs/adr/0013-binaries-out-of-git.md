---
status: accepted
---

# ADR-13 — Binaries out of git; iCloud is their backup

Supersedes the git part of ADR-11 and ADR-12. By October the vault held about 830 files, but git tracked 20: ADR-4 commits only what horde writes, so everything added in Obsidian stayed untracked, and there was no offsite backup at all. Committing the 260M of manuals, photos and video would make `.git/` the bulk of the vault for no diffing benefit.

**Decision:** the vault root `.gitignore` allowlists notes (`*.md`, `*.canvas`) and ignores everything else. Horde's git layer skips ignored paths: `create_attachment` and `move_attachment` return an empty `commit`, and `delete_attachment` removes the file without a commit or `restore_hint`. A nightly user timer (`obsidian-backup.timer`) runs `rclone sync` of the whole vault, `.git/` included, to iCloud Drive (`apples:horde-backup/Obsidian`) with `--backup-dir apples:horde-backup/versions/<date>`.

**Consequences:** `history`/`restore` cover notes only. A binary deleted or overwritten the same day it was added, before a nightly run, is not recoverable. iCloud sessions expire, so the service raises a desktop notification on failure; the fix is `rclone config reconnect apples:`.
