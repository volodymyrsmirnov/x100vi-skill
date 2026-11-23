# Working on this repository

`x100vi` is a Python package (CLI + library) plus an agent skill in `skills/x100vi/`.

## Commands

```bash
uv sync                 # install
uv run pytest           # all tests; no camera needed
uv run ruff check       # lint
X100VI_HARDWARE=1 uv run pytest tests/hardware -v   # needs a real X100VI, see that file's docstring
```

## Layout

- `src/x100vi/model.py` - recipe fields, validation, wire encoding. Every table in it was measured on a
  camera; do not add a value without evidence (see `docs/protocol.md`).
- `src/x100vi/camera.py` - identity checks, read, verified write.
- `src/x100vi/rawconv.py` - RAF upload and in-camera conversion.
- `src/x100vi/ptp.py` - PTP over USB; the only module that touches hardware.
- `src/x100vi/cli.py` - commands; text for people, `--json` for agents.
- `tests/fake_camera.py` - a software X100VI reproducing the real body's quirks. New camera behaviour
  goes here first, with a test.
- `skills/x100vi/` - the skill; shipped inside the wheel as `x100vi/_skill`. `scripts/x100vi.sh` and
  `scripts/x100vi.ps1` are the launchers that install the tool on first use; the skill tells agents to call
  the tool only through them. Keep both in step (`tests/test_launcher.py` covers the POSIX one).

## Rules

- Never add firmware-update operations or anything that writes outside device properties and the
  RAW-conversion upload.
- A write is not done until it has been read back and compared. Keep that property.
- Every user-facing error is an `X100VIError` with a `hint` a non-technical person can act on.
- Checks that need no camera (recipe validation, RAF header) run before connecting.
- Keep `src/x100vi/recipe.schema.json`, `model.py` and `skills/x100vi/references/recipe-schema.md` in step;
  `tests/test_library.py` checks the first two.
- Do not commit photographs or RAF files.
