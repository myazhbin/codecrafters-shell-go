---
name: codecrafters-stage-workflow
description: How the user works through CodeCrafters shell stages in this repo, what they submit themselves, and where the stages' spec and bash differ
metadata:
  type: project
---

The user pastes each CodeCrafters "Build your own shell" stage (description and tests) and wants it implemented in app/main.go (Go). They run `codecrafters submit` themselves, so don't commit or submit unless asked. That command commits every untracked file; the user confirmed on 2026-10-25 that committing the .claude/ folder with submissions is fine, so don't suggest gitignoring it.

**Why:** they review and submit each stage on their own; `.idea/` also went in through a submit.
**How to apply:** implement the stage, add its tests to src/main_test.go (the user asked on 2026-10-26 for every test to live there), run `go test ./src/`, report, and give the `codecrafters submit` command. See [[macos-shell-testing-pitfalls]].
- To check behavior against bash by hand, use bash 5.3 at /usr/local/bin/bash (/bin/bash is 3.2).
- Where a stage's spec and bash disagree, the spec wins. Seen so far: no bell when Tab extends to the common prefix (bash rings), `complete -r` silent for an unregistered name (bash errors), the two-space match list, and `type`'s "<name>: not found" (bash: "type: <name>: not found"). macOS bash also pads the `jobs` status column to 27 where the spec (Linux bash) says 24.
- Bash notices a job's exit asynchronously, so right after a job ends its `jobs` can still say Running; the tests wait for the process to be reaped before checking.
- Job markers: the stages define them by position (highest number +, next -), and the shell does that. Bash instead gives - to the newest job that was still running when the current job started, so they differ when a job ends before a newer one starts (bash: `[2]   Done`, ours: `[2]-  Done`). TestReapingBeforePrompt pins the stages' version; check this first if a later stage fails on markers.
- Builtins in a pipeline run in the shell process, one at a time after the programs start, so `cd` there changes the shell's directory, unlike bash's subshells; `exit` there is ignored, as in bash.
