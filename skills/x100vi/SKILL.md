---
name: x100vi
description: Create, preview and manage film-simulation recipes ("presets", "looks") on a Fujifilm X100VI over USB with the x100vi CLI. Use when the user wants a recipe for their X100VI from exact settings, from a recipe web page, or from a plain description such as "I'm going to Dubai and want cool photos of deserts"; wants to save recipes into the camera's custom settings C1-C7; wants the camera's current settings read out or backed up as JSON; or wants to see how a RAF file would look with a recipe, rendered by the camera's own processor.
---

# Fujifilm X100VI recipes

You drive the `x100vi` command-line tool. It talks to the camera over USB and does three things:

1. **Reads** the live shooting settings and the custom settings C1-C7 as JSON recipes.
2. **Writes** a recipe to the live settings or to one of C1-C7, then reads it back to prove it stuck.
3. **Previews**: uploads a RAF to the camera, has the camera's own processor develop it with a recipe,
   and returns the JPEG. Nothing is saved on the camera.

A recipe is a small JSON file. Field reference and how to translate recipe-site wording:
[references/recipe-schema.md](references/recipe-schema.md). How to design a look from a description:
[references/recipe-design.md](references/recipe-design.md). What every error means:
[references/troubleshooting.md](references/troubleshooting.md).

## Running the tool

Run every command through the launcher that ships in this skill's folder, next to this file:

```bash
sh "<this skill's folder>/scripts/x100vi.sh" doctor --json
```

On Windows: `powershell -ExecutionPolicy Bypass -File "<this skill's folder>\scripts\x100vi.ps1" doctor --json`.

The first call downloads the tool (one small binary from the project's latest GitHub release, checksum
verified) into a cache folder; later calls reuse it. Download messages go to stderr; stdout stays clean
JSON. **Everywhere below, `x100vi` means this launcher.**

- If the launcher exits 127, the download failed (no network, or no release for this system). Relay its
  error and hint.
- If a command or option described here is reported as unknown, the installed tool is older than this
  skill: run the launcher with `--self-update`, then retry.

## Camera commands must run outside the sandbox

Your shell commands may run in a sandbox. A sandbox hides USB devices, so inside one the camera looks
absent however well it is connected. Every command that talks to the camera needs to run **outside** the
sandbox: `doctor`, `get`, `set`, `backup`, `restore`, `preview`.

- Request unsandboxed execution for these commands from the very first call, giving the reason "needs USB
  access to the camera". In Codex that is an escalated-permissions request for the command; in Claude Code
  it is running the command with the sandbox disabled.
- If a command answers `sandboxed` (exit 14), run the **same command** again outside the sandbox. Do the
  same if it answers `camera_not_found` while the user says the camera is connected.
- Never tell the user to check the cable, the power or the USB mode on the strength of a sandboxed result.
  It says nothing about the camera.
- `validate`, `recipes` and `schema` need no camera and are fine inside the sandbox.
- If the user is tired of approving, tell them `x100vi sandbox-help` prints the settings that allow these
  commands permanently (Codex rules, Claude Code settings). Applying them is the user's decision: do not
  edit agent configuration yourself.

## Before you start

1. Check the tool: `x100vi --version`.
2. Check the camera: `x100vi doctor --json`. If `ready` is false, relay the `message` and `hint` to the
   user in plain words and stop until they fix it. The usual cause is the USB mode, which only the user
   can change on the camera body: **MENU > NETWORK/USB SETTING > CONNECTION MODE > USB RAW CONV./BACKUP RESTORE**,
   then replug the cable.

Designing and validating recipes needs no camera. Only `doctor`, `get`, `set`, `backup`, `restore` and
`preview` do.

## What the user asks for, and what you do

### "Give me a look for ..." (a description, not settings)

1. Read [references/recipe-design.md](references/recipe-design.md). Ask at most one question, and only if
   the answer changes the look (colour or black and white; time of day).
2. Write two or three distinct candidate recipes as JSON files in the working directory. Give each a
   `name` (25 characters, plain ASCII), a one-line `description`, and `notes` with the ISO and exposure
   compensation the user should set on the body. `x100vi recipes --json` lists bundled starting points
   you may adapt; say so when you start from one.
3. Validate: `x100vi validate a.json b.json`. Fix anything it rejects.
4. If the user has a RAF from their X100VI and the camera is ready, preview (next section) and show them
   the contact sheet. Otherwise describe each candidate in a sentence and say a preview needs a RAF.
5. Let the user choose. Then save it (section "Save it to the camera").

### "Use these settings" / "use this recipe page"

Translate into JSON with [references/recipe-schema.md](references/recipe-schema.md), keep the source URL in
`source`, validate, then preview or save as asked. Settings the camera cannot take over USB (ISO, exposure
compensation, anything about focus or drive) go into `notes`; tell the user to set them on the body.

### "Show me how this photo would look"

```bash
x100vi preview shot.RAF --as-shot -r desert-gold my-look.json --json
```

- `-r` takes recipe files or bundled starter names. One upload serves all recipes; several RAFs are fine.
- Output goes to `./x100vi-previews/` (`-o DIR` to change): one JPEG per RAF and recipe, plus
  `contact-sheet.jpg`. Show the contact sheet to the user; open individual files when they want detail.
- Previews are half size. Add `--full` only when the user wants a full-resolution file.
- `--format heif` gives HEIF files. `--format tiff` / `tiff16` give 8- / 16-bit TIFF and need `--full`
  (about 125 / 245 MB each). Use them only when the user asks for that format; the contact sheet is made
  for JPEG only.
- `--ev 0.67` applies exposure compensation (1/3 EV steps, -2 to +3) when the recipe's notes call for it.
- `--d-range-priority weak|strong|plus|off` renders with that D-range priority instead of the recipe's.
  A recipe with `d_range_priority: auto` can only be previewed this way. `plus` is a level beyond strong
  that only the converter offers: it cannot be saved to the camera, so never put it in a recipe.
- **Always report `not_applied`.** Each entry says which setting the render could not honour for this
  particular frame and why, for example a DR400 recipe on a frame shot at DR200. Say it in plain words
  and make clear the recipe itself is unchanged and will behave as written when shooting.
- `--verify` compares each output's EXIF with its recipe (needs `exiftool`).

### "Save it to the camera"

1. Ask which custom setting (C1-C7) unless the user already said. `x100vi get all` shows what each holds.
2. `x100vi set C3 my-look.json --json`
3. If it fails with `slot_occupied`, tell the user what is in that slot and ask whether to replace it or
   pick another. Only after an explicit yes for that slot: add `--overwrite`. The previous contents are
   saved automatically and the result reports where (`previous_saved_to`).
4. Relay `advice` and `set_on_camera` from the result, and the recipe's `notes`.

`x100vi set live my-look.json` changes the current shooting settings instead of a custom setting.
`--partial` changes only the settings present in the file; without it every look setting not in the file
is reset to the camera default, so a recipe always produces the same look.

### "What is on my camera?" / "back it up"

```bash
x100vi get C2            # one custom setting, as a recipe
x100vi get live          # current shooting settings
x100vi backup -o my-camera.json     # live + C1-C7 in one file
x100vi restore my-camera.json --yes # writes C1-C7 back; ask the user before running this
x100vi backup --full -o camera.dat  # the camera's own settings file: every menu setting plus C1-C7
x100vi restore camera.dat --yes     # replaces EVERY setting on the camera; ask first
```

Use `--full` when the user wants everything saved, for example before a trip or a firmware update. Tell
them the file is private: it holds the camera's ID and network settings. Restoring a `--full` file
replaces all menus, the live settings and C1-C7. It is refused for another model, other firmware or
another body; `--other-body` overrides only the last check and needs the user's explicit yes.

## Rules

- **Ask before every write to the camera** (`set`, `restore`), unless the user already asked for exactly
  that write. Reading, validating and previewing need no confirmation.
- **Never pass `--overwrite`, `restore --yes` or `--other-body` on your own judgment.** They replace the
  user's saved looks or settings.
- Do not invent settings. The camera accepts only what the schema lists; `validate` is the authority.
- One `x100vi` command at a time: a second one waits for the first (or fails with `camera_busy`). On Linux
  no other camera program may hold the camera; on macOS and Windows other apps may stay open.
- RAF files must come from an X100VI. JPEGs cannot be previewed.
- If `preview` reports a conversion timeout, do not run it again. Tell the user to switch the camera off
  and on first.
- Recipes are taste. Present candidates as options, show previews when you can, and do not claim a
  recipe reproduces a specific film stock.

## Reading results

With `--json` every command prints one object. Errors print `{"error": kind, "message": ..., "hint": ...}`
and exit non-zero; the `hint` is written for the user, so pass it on.

| Exit | `error` | Meaning |
|---|---|---|
| 0 | | done |
| 2 | `invalid_recipe` | the recipe or an argument is not valid; nothing was sent |
| 3 | | `verify`: the photo does not match the recipe |
| 4 | `slot_occupied` | the custom setting holds a recipe; needs the user's decision |
| 5 | `missing_dependency` | an optional tool (exiftool) is not installed |
| 10 | `camera_not_found` | no camera on USB |
| 11 | `wrong_usb_mode` | camera is not in USB RAW CONV./BACKUP RESTORE |
| 12 | `unsupported_camera` | not an X100VI |
| 13 | `camera_busy` | another program, or another `x100vi` command, holds the camera |
| 14 | `sandboxed` | the command ran inside a sandbox, where USB is hidden; rerun it outside |
| 15 | `camera_not_responding` | the camera stopped answering; ask the user to switch it off and on (or remove the battery) before anything else |
| 20 | `conversion_failed` | the RAF could not be developed |
| 30 | `camera_refused` | the camera rejected a request |
