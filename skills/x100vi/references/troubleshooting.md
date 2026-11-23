# Troubleshooting

Start with `x100vi doctor`. Every error prints a `hint`; the table adds what usually lies behind it.

| Exit | `error` | What happened | What to tell the user |
|---|---|---|---|
| 10 | `camera_not_found` | No Fujifilm device on USB | Use a data cable (some USB-C cables only charge), switch the camera on, wake it if it slept. If the camera is in USB WEBCAM mode it is not reachable. |
| 11 | `wrong_usb_mode` | The camera is in USB CARD READER or another mode | On the camera: MENU > NETWORK/USB SETTING > CONNECTION MODE > USB RAW CONV./BACKUP RESTORE, then unplug and replug. The X100VI has no tether mode; this is the only mode that exposes settings. |
| 12 | `unsupported_camera` | A different Fujifilm body is connected | Only the X100VI is supported. Other bodies use different property codes; writing to them is refused on purpose. |
| 14 | `sandboxed` | The command ran inside the agent's sandbox, which hides USB devices | Nothing is wrong with the camera. The agent must run the same command outside the sandbox; approve its request. See "Agent sandboxes" below. |
| 13 | `camera_busy` | Another program has claimed the camera | Quit FUJIFILM X RAW STUDIO, X Acquire, Lightroom, Capture One. On Linux, unmount the camera in the file manager. |
| 15 | `camera_not_responding` | The camera stopped answering mid-session | Switch it off and on and reconnect. If the power switch does nothing, take the battery out for a few seconds. Do not retry before that. |
| 2 | `invalid_recipe` | A recipe value or command argument is not acceptable | The message names the setting and the allowed values. Nothing was sent to the camera. |
| 4 | `slot_occupied` | The custom setting already holds a recipe | Ask whether to replace it or use another slot. `--overwrite` saves the old contents first. |
| 5 | `missing_dependency` | `exiftool` is not installed | Only `verify` and `preview --verify` need it. |
| 20 | `conversion_failed` | The RAF could not be developed | See the next section. |
| 30 | `camera_refused` | The camera answered a request with an error | Run `x100vi doctor`; if it persists, power-cycle the camera. |

## Agent sandboxes

Codex, and Claude Code when its sandbox is switched on, can run shell commands in a sandbox. USB devices
are invisible there, and so is the network.

- The tool recognises Codex's sandbox and answers `sandboxed` (exit 14) instead of "no camera found".
  Where it cannot tell (Claude Code's sandbox sets no marker), `camera_not_found` adds a note when no USB
  device of any kind is visible.
- **In the moment**: the agent asks to run the command outside the sandbox; approve it.
- **Permanently**: `x100vi sandbox-help` prints ready-made settings for this machine.
  - Codex: a rules file for `~/.codex/rules/x100vi.rules` (`x100vi sandbox-help --write-codex-rules` writes
    it; restart Codex). Reading and previewing then run unsandboxed without a prompt; `set`, `restore` and
    `--self-update` still ask each time.
  - Claude Code: a `sandbox.excludedCommands` fragment for `~/.claude/settings.json`.
- The first-use install of the tool also fails in a sandbox (no network); the launcher says so with exit 14.

## The tool will not install or run

The skill runs the tool through `scripts/x100vi.sh` (Windows: `scripts/x100vi.ps1`), which downloads the
binary for this system from the latest GitHub release on first use and caches it (macOS
`~/Library/Caches/x100vi`, Linux `~/.cache/x100vi`, Windows `%LOCALAPPDATA%\x100vi`).

- **Exit 127, "could not download"**: no network, or GitHub unreachable. Try again later, or download the
  binary from https://github.com/volodymyrsmirnov/x100vi-skill/releases and set `X100VI_BIN` to its path.
- **"does not match the release checksum"**: the download was damaged or tampered with; nothing was
  installed. Run the command again.
- **A command or option is unknown**: the cached tool is older than the skill. Run the launcher with
  `--self-update`.
- `X100VI_BIN` runs a specific binary (e.g. one built from a checkout); `X100VI_RELEASE_URL` downloads from
  somewhere else.

## Preview problems

- **"was shot on a ..., not an X100VI"**: the camera only develops RAFs from its own model.
- **"is a JPEG"**: previews need the RAW file. Shoot RAW or RAW+JPEG.
- **"did not accept this RAF"**: the file is truncated, edited, or not a real X100VI RAF.
- **"produced no image within 90 s"**: the conversion did not finish. Do not retry. Switch the camera off
  and on (take the battery out briefly if it will not switch off), reconnect, then try again.
- **A setting listed under `not_applied`**: not an error. That frame cannot show that setting, usually
  because of how it was shot:
  - dynamic range above the frame's own (a frame shot at DR100 or DR200 cannot be developed at DR400);
  - `dynamic_range: auto`, rendered as 200 or the frame's own value, whichever is lower;
  - `dynamic_range`, `highlight`, `shadow` ignored because the recipe turns D-range priority on;
  - `d_range_priority` replaced because `--d-range-priority` was given.
- **"d_range_priority auto cannot be previewed"**: add `--d-range-priority weak`, `strong` or `plus`.
- **"exposure ... is not valid for a conversion"**: the camera develops only -2 to +3 EV, in 1/3 steps.
- **Low battery warning**: conversion runs on the camera's battery and USB power saving is off in this mode.
- **"TIFF previews need --full"**: the camera makes TIFF only at full resolution. Add `--full`, or use
  `--format jpeg` / `heif` for half-size previews.
- **"returned something other than ..."**: this firmware did not produce the requested format; use JPEG.

## Settings file problems (`backup --full`, restoring a `.dat`)

- **"not a camera settings file"**: the file was not saved by `x100vi backup --full`. A JSON backup is
  restored with the same command, but it only covers C1-C7 (and live settings with `--live`).
- **"from an X-..., not an X100VI"**, **"different size"**: the file comes from another model or other
  firmware. Restore it only on a matching camera and firmware.
- **"saved from a different X100VI body"**: it also carries that body's network settings. `--other-body`
  restores it anyway; only use it when the user explicitly agrees.
- **"reads back different settings"**: switch the camera off and on, then check its settings. The previous
  file is saved in the backups folder and can be restored the same way.

## Things that are normal

- The camera screen shows a USB connection screen and its controls are locked while connected in this mode.
- `get live` omits `image_size` while the camera shoots RAW only.
- White-balance shift is remembered per white-balance mode by the camera. A recipe sets the shift of the
  mode it uses.
- Saving a recipe resets look settings it does not mention to their defaults. Use `--partial` to change
  only some settings.
- Automatic backups are kept in `~/.local/state/x100vi/backups` (Windows: `%LOCALAPPDATA%\x100vi\backups`;
  override with `X100VI_HOME`).

## Platforms

- **macOS**: works out of the box, through macOS' camera service (ImageCaptureCore). Other camera apps
  may stay open.
- **Windows**: not tested on hardware. Works through Windows Portable Devices with Windows' own driver;
  no extra driver is needed. The camera should appear in Explorer under This PC.
- **Linux**: expected to work, not tested on hardware. Uses libusb. The desktop may auto-mount the camera
  (gvfs): unmount it first. Your user needs access to the USB device: a udev rule for vendor `04cb`, or
  `sudo`.
- `X100VI_TRANSPORT=usb` forces libusb anywhere. On macOS it stops `ptpcamerad` first; on Windows it needs
  the WinUSB driver from [Zadig](https://zadig.akeo.ie), and while that is installed Explorer and
  X RAW STUDIO no longer see the camera.

## What is never done

The tool sends only standard PTP property reads and writes plus the RAW-conversion upload that X RAW STUDIO
uses. It does not update firmware and has no code path that could.
