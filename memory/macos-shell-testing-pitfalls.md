---
name: macos-shell-testing-pitfalls
description: macOS and test-harness quirks that break local tests of the shell but are not shell bugs
metadata:
  type: project
---

Testing this shell on the user's Mac (macOS 15). src/main_test.go already works around all of these:
- Copies of system binaries such as /bin/cat placed in another folder are killed with SIGKILL (kernel log: "AMFI: Launch Constraint Violation"). Use a `#!/bin/sh` wrapper (`exec cat "$@"`) or a self-built binary instead.
- A pty test harness must keep reading the terminal's output until the shell exits. Otherwise, after a /bin/sh script has run, the shell hangs in the kernel while closing the terminal on `exit` (unkillable, hidden from ps).
- Background jobs (`sleep 500 &`) hold the pty open after the shell exits, so a harness waiting for the terminal to close stalls until they end. The tests kill the shell's process group, which its jobs are in.
- Python ignores SIGPIPE, and a shell started from Python with `pty.fork()` + `execve` inherits that, as do its children (Go resets it for programs it starts; bash doesn't), so bash's pipelines misbehave there (e.g. `tail -f | head` never ends). Reset it to SIG_DFL in the child before exec. The Go tests don't have this problem.
- Only BSD tail is installed: `tail -f x | head -n 5` ends only after one more line is written to x, in bash too. GNU tail on the Linux tester exits as soon as head does.
- Readline redraws the whole line on every key, so raw pty bytes are unreadable; the tests feed them through a small screen emulator.

**Why:** the first two cost a long investigation on 2026-10-25 before being traced to macOS rather than the shell; the SIGPIPE one made bash look broken on 2026-10-26.
**How to apply:** keep these in mind when changing the tests or chasing a hang, and don't treat these symptoms as shell bugs. Related: [[codecrafters-stage-workflow]].
