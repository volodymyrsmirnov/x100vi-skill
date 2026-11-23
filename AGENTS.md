# Working on this repository

`x100vi` is a Go command-line tool plus an agent skill in `skills/x100vi/`. Every push to `main` publishes
a GitHub release with binaries for macOS (universal), Linux (amd64, arm64) and Windows (amd64); the skill's
launchers download them.

## Commands

```bash
go test ./...                                   # all tests; no camera needed
go vet ./... && gofmt -l .                      # checks CI runs
GOOS=windows go vet ./... && GOOS=linux GOARCH=arm64 go vet ./...   # other platforms
go build -o x100vi ./cmd/x100vi
X100VI_HARDWARE=1 X100VI_TEST_RAF=/path/to/shot.RAF go test ./internal/hardware -v   # needs a real X100VI
```

## Layout

- `internal/model` - recipe fields, validation, wire encoding. Every table in it was measured on a camera;
  do not add a value without evidence (see `docs/protocol.md`).
- `internal/camera` - identity checks, read, verified write, the camera's settings file.
- `internal/rawconv` - RAF upload and in-camera conversion.
- `internal/ptp` - PTP datasets and the session (including the guard against GetObject before
  GetObjectInfo, which hung a camera).
- `internal/transport` - the only code that touches hardware: ImageCaptureCore on macOS (cgo), Windows
  Portable Devices on Windows (COM), usbfs on Linux.
- `internal/sandbox` - agent-sandbox detection and the Codex rules / Claude Code settings `sandbox-help`
  prints.
- `internal/cli`, `cmd/x100vi` - commands; text for people, `--json` for agents.
- `internal/fakecam` - a software X100VI reproducing the real body's quirks. New camera behaviour goes here
  first, with a test.
- `assets/` - starter recipes and the recipe JSON schema, embedded in the binary.
- `skills/x100vi/` - the skill, also embedded (`x100vi install-skill`). `scripts/x100vi.sh` and
  `scripts/x100vi.ps1` download the binary on first use; the skill tells agents to call the tool only
  through them. Keep both in step.

## Rules

- Never add firmware-update operations or anything that writes outside device properties, the
  RAW-conversion upload and the settings-file restore. Never write property `0xD17B` (it powers the camera
  off).
- A write is not done until it has been read back and compared. Keep that property.
- Every user-facing error is an `errs.Error` with a hint a non-technical person can act on.
- Checks that need no camera (recipe validation, RAF header) run before connecting.
- Keep `assets/recipe.schema.json`, `internal/model` and `skills/x100vi/references/recipe-schema.md` in
  step; the library tests check the first two.
- Do not commit photographs or RAF files.
