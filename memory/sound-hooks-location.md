---
name: sound-hooks-location
description: The user's Claude Code sound hooks (all projects) play scripts stored in this repo's .claude/sounds
metadata:
  type: reference
---

Since 2026-10-25, ~/.claude/settings.json has a Stop hook running .claude/sounds/play.sh (done.mp3) and a PermissionRequest hook running .claude/sounds/play_approval.sh (meow.mp3), both by absolute path into /Users/zed/Desktop/codecrafters-shell-go. The user chose user-level settings so the sounds play in every project. The commands end with `2>/dev/null || true`, so moving or deleting this repo silences the sounds without errors.

Related: [[codecrafters-stage-workflow]].
