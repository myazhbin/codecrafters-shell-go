# CodeCrafters shell in Go

The whole shell is `app/main.go`. Its tests are `src/main_test.go`.

## Stages

- The user pastes each CodeCrafters "Build your own shell" stage. Implement it in `app/main.go`, add tests for it to `src/main_test.go`, run them, and report.
- Don't commit or run `codecrafters submit` unless asked. The user submits, which commits every file in the repo and pushes it.
- Where a stage's spec and bash disagree, follow the spec.

## Tests

- All tests live in `src/main_test.go`. Add new ones there for every stage, and update the ones a stage changes.
- Run them from the repo root with `go test ./src/`. `TestMain` builds `app/` itself, with the race detector when the toolchain supports it.
- Most tests drive the shell on a pseudo-terminal, without third-party packages, and check what the screen shows. `TestScriptsMatchBash` pipes the same command lines into the shell and into bash and compares what they print.

## Memory

This project's memory lives in this repo, not in `~/.claude`. `MEMORY.md` is the index, and each memory is a file in `memory/`. Read the ones that bear on the work, and save new memories and updates there.

@MEMORY.md
