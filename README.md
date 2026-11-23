# x100vi-skill

> 📝 Blog post: [Fujifilm X100VI AI skill: film recipes over USB with Claude Code and Codex](https://volodymyr.smirnov.im/writing/x100vi-skill/)

Create film-simulation recipes for a **Fujifilm X100VI** by describing the look you want, see them
developed from your own RAW files by the camera's processor, and save the ones you like into the camera's
custom settings (C1-C7), all over a USB cable.

It is two things in one repository:

- **`x100vi`**, a command-line tool that reads, writes and previews recipes on the camera. It is a single
  self-contained binary for macOS, Linux and Windows.
- **An agent skill** for [Claude Code](https://code.claude.com) and [Codex](https://github.com/openai/codex)
  that drives the tool for you, so you can say:

  > I'm going to Dubai and want cool photos of deserts. Show me three options on this RAF and put the
  > best one in C3.

**Previews are developed by the camera itself.** The RAF is sent to the camera over USB, the camera's own
image processor develops it with the recipe applied, and the result comes back. Nothing on the computer
imitates Fujifilm's processing, so what you see is what the camera would have produced.

No firmware is touched. The tool reads and writes the same settings you can change in the camera's menu,
and uses the camera's built-in RAW conversion.

> [!IMPORTANT]
> **Put the camera in the right USB mode before you connect it.** On the camera:
> **MENU > NETWORK/USB SETTING > CONNECTION MODE > USB RAW CONV./BACKUP RESTORE**, then plug in the cable.
>
> Nothing here works in any other mode, and software cannot change the mode for you. In USB CARD READER
> mode, which is what most cameras are set to, the camera hides all of its settings. See [Set up the camera](#set-up-the-camera-required).

## What it can do

| | |
|---|---|
| Read | The current shooting settings and C1-C7, as JSON recipes. |
| Write | A recipe to the live settings or to any of C1-C7, including its name. Every write is read back and compared. |
| Preview | Develop RAFs on the camera with one or more recipes, as JPEG (with a contact sheet), HEIF, or full-size 8/16-bit TIFF. Nothing is stored on the camera afterwards. |
| Back up | C1-C7 and the live settings as JSON, or the camera's own settings file with every menu setting, and restore either. |
| Design | With the skill: turn a description, a list of settings, or a recipe web page into a recipe. |

What a recipe controls: film simulation, grain, colour chrome effect, colour chrome FX blue, smooth skin,
white balance (mode, Kelvin, red/blue shift), dynamic range, D-range priority, highlight, shadow, colour,
sharpness, noise reduction, clarity, monochromatic tint, and optionally image quality, image size, colour
space and lens modulation optimizer.

What it cannot control, because the camera does not expose it over USB: ISO, exposure compensation,
aperture, shutter, focus and AF settings, drive mode, metering, flash. Recipes carry these as `notes` for
you to set on the body.

## Install

You need a Fujifilm **X100VI** (tested on firmware 1.31) and a USB-C **data** cable. Optional:
[exiftool](https://exiftool.org), only for `x100vi verify` and `preview --verify`.

### The skill

With the [skills](https://www.npmjs.com/package/skills) CLI (needs Node.js 22.20 or later):

```bash
npx skills add volodymyrsmirnov/x100vi-skill -g
```

It asks which agents to install for. To skip the questions:

```bash
npx skills add volodymyrsmirnov/x100vi-skill -g -a claude-code -a codex -y
```

`-g` makes the skill available in every project; leave it out to install into the current project only.
Start a new agent session afterwards.

**That is all you need.** The first time the agent uses the skill, its launcher downloads the `x100vi`
binary for your system from the [latest release](https://github.com/volodymyrsmirnov/x100vi-skill/releases/latest),
checks it against the release's checksums, and keeps it in a cache folder. Ask the agent to update the
tool (or run the launcher with `--self-update`) to fetch the newest release.

Other ways to get the skill:

- **Claude Code plugin**: `/plugin marketplace add volodymyrsmirnov/x100vi-skill`, then
  `/plugin install x100vi@x100vi-skill`.
- **From the tool**: `x100vi install-skill --claude --codex`, or `--dir SKILLS_FOLDER` for any agent that
  reads `SKILL.md` folders.

### The command-line tool on its own

Download the binary for your system from the
[latest release](https://github.com/volodymyrsmirnov/x100vi-skill/releases/latest) and put it on your
PATH as `x100vi` (`x100vi.exe` on Windows):

| System | File |
|---|---|
| macOS (Apple silicon and Intel) | `x100vi-darwin-universal` |
| Linux x64 / arm64 | `x100vi-linux-amd64` / `x100vi-linux-arm64` |
| Windows x64 | `x100vi-windows-amd64.exe` |

For example on macOS:

```bash
curl -fL -o /usr/local/bin/x100vi https://github.com/volodymyrsmirnov/x100vi-skill/releases/latest/download/x100vi-darwin-universal
```

```bash
chmod +x /usr/local/bin/x100vi && x100vi --version
```

A file downloaded with a web browser instead is quarantined by macOS; clear that with
`xattr -d com.apple.quarantine /usr/local/bin/x100vi`. `SHA256SUMS` in each release lists the checksums.
Every push to `main` publishes a new release.

### Set up the camera (required)

Do this on the camera body **before** connecting. It has to be done by hand: the mode cannot be changed
over USB.

1. Press **MENU/OK** and open **NETWORK/USB SETTING**.
2. Choose **CONNECTION MODE**, then **USB RAW CONV./BACKUP RESTORE**.
3. Connect the camera to the computer with the USB-C cable and switch it on.
4. If the cable was already plugged in when you changed the mode, unplug it and plug it back in.

The camera keeps this setting, so you only repeat it if you switch to another mode in between (for
example USB CARD READER to copy photos). The X100VI has no tether mode; this is the only USB mode that
exposes settings.

Check it:

```bash
x100vi doctor
```

```text
X100VI firmware 1.31
USB mode: USB RAW CONV./BACKUP RESTORE
battery: about 80%
ready
```

In the wrong mode nothing is read or written, and you are told what to change:

```text
not ready: the camera is in USB CARD READER, so its settings are not reachable
  On the camera: MENU > NETWORK/USB SETTING > CONNECTION MODE > USB RAW CONV./BACKUP RESTORE. Then unplug
  and replug the USB cable.
```

While connected in this mode the camera shows a USB screen and its controls are locked; unplug it to
shoot. Switch CONNECTION MODE back when you are done if you normally use another.

## Using the skill

With the camera in **USB RAW CONV./BACKUP RESTORE** mode and connected, just ask. If the mode is wrong, the
agent stops and tells you how to change it. Examples of what the skill handles:

- "Make me a moody black-and-white street look and show it on `DSCF4158.RAF`."
- "Use this recipe: <link to a recipe page>. Put it in C2 and call it PORTRA."
- "What's in my C1 to C7? Save them to `my-camera.json`."
- "Here are three RAFs from Dubai; show me how they'd look with a warm desert look and a muted one."

It asks before writing to the camera, never replaces a custom setting that already holds a recipe without
your say-so, and tells you when a preview could not show a setting because of how the photo was taken.

### Agent sandboxes

Codex (and Claude Code, if you turned its sandbox on) can run commands in a sandbox, and a sandbox hides
USB devices. The camera commands therefore have to run **outside** it. The skill tells the agent to ask for
that, and the tool reports `sandboxed` instead of a misleading "no camera found", so normally you just
approve the request when it appears.

To stop being asked, run this once in a terminal:

```bash
x100vi sandbox-help
```

It prints ready-made settings for your machine: a Codex rules file (`--write-codex-rules` saves it to
`~/.codex/rules/x100vi.rules`; restart Codex afterwards) and a `sandbox.excludedCommands` fragment for
Claude Code's `~/.claude/settings.json`. With the Codex rules, reading and previewing run without a
prompt, while `set` and `restore` still ask before each run.

## Using the tool directly

Every command that talks to the camera (`doctor`, `get`, `set`, `backup`, `restore`, `preview`) needs it
connected in **USB RAW CONV./BACKUP RESTORE** mode. `recipes`, `validate`, `verify` and `schema` work without
a camera.

```bash
x100vi recipes                               # bundled starter recipes
x100vi validate my-look.json                 # check a recipe, no camera needed
x100vi get all                               # live settings and C1-C7 as JSON
x100vi get C3 -o c3.json                     # one custom setting to a file
x100vi set C3 my-look.json                   # save a recipe (refuses if C3 is occupied)
x100vi set C3 my-look.json --overwrite       # replace it; the old one is saved first
x100vi set live desert-gold                  # apply a starter recipe to the current settings
x100vi backup -o my-camera.json              # live settings and C1-C7 as JSON
x100vi restore my-camera.json --yes          # write C1-C7 back
x100vi backup --full -o camera.dat           # the camera's own settings file: every menu setting too
x100vi restore camera.dat --yes              # put every setting back
x100vi preview shot.RAF --as-shot -r desert-gold street-mono my-look.json
x100vi sandbox-help                          # how to let an agent reach the camera from its sandbox
```

Add `--json` to any command for machine-readable output. `x100vi COMMAND --help` lists every option.

### A recipe

```json
{
  "name": "DESERT GOLD",
  "description": "Warm, sun-baked sand under a deep blue sky.",
  "notes": "Exposure compensation +1/3 to +2/3 on bright sand. ISO auto up to 3200.",
  "film_simulation": "classic_chrome",
  "dynamic_range": "200",
  "grain": { "strength": "weak", "size": "small" },
  "color_chrome_effect": "strong",
  "color_chrome_fx_blue": "strong",
  "white_balance": { "mode": "kelvin", "kelvin": 6200, "shift_r": 2, "shift_b": -3 },
  "highlight": -1,
  "shadow": 1,
  "color": 2,
  "sharpness": 0,
  "noise_reduction": -4
}
```

Every field, its range and the camera's rules: [skills/x100vi/references/recipe-schema.md](skills/x100vi/references/recipe-schema.md).
`x100vi schema` prints the JSON Schema. The bundled starter recipes are original starting points, not
emulations of any film stock.

### Previews

```bash
x100vi preview DSCF4103.RAF DSCF4158.RAF --as-shot -r desert-gold desert-dusk street-mono
```

```text
DSCF4103.RAF: shot as classic_chrome, DR200, D-range priority weak
  x100vi-previews/DSCF4103__as-shot.jpg
  x100vi-previews/DSCF4103__desert-gold.jpg
  x100vi-previews/DSCF4103__desert-dusk.jpg
    not applied as written - dynamic_range: asked for 400, rendered with 200 (this frame was shot at
    DR200; a RAF cannot be developed above its own dynamic range. On the camera the recipe will use
    the full value when ISO allows it)
  ...
contact sheet: x100vi-previews/contact-sheet.jpg
```

- **The conversion runs on the camera, not on the computer.** The tool uploads the RAF, tells the camera
  which settings to use, waits while the camera develops it, and downloads the result. That is why the
  camera must be connected and switched on, why it uses the camera's battery, and why the output matches
  an in-camera JPEG.
- The RAF must come from an X100VI. It is uploaded once and developed once per recipe.
- Output is half size by default; `--full` gives full resolution. `--ev 0.67` adds exposure compensation
  (-2 to +3 EV in 1/3 steps; the camera ignores anything outside that when developing).
- `--d-range-priority weak|strong|plus|off` previews with that D-range priority instead of the recipe's.
  It is the way to preview a recipe set to Auto. `plus` is an extra level beyond strong that only the
  conversion offers; the camera cannot shoot with it.
- `--format heif` makes HEIF files. `--format tiff` (8-bit) and `--format tiff16` need `--full`, because
  the camera makes TIFF only at full resolution: about 125 MB and 245 MB per image. The contact sheet is
  made for JPEG previews only.
- A preview shows colour and tone on a frame you already shot. It cannot show what a different ISO or
  exposure at capture would have done.

## How it reaches the camera

The tool uses each system's own interface for cameras, so there are no drivers or libraries to install:

| | Interface | Status |
|---|---|---|
| macOS | ImageCaptureCore, the system camera service. | Tested on a real X100VI. |
| Windows | Windows Portable Devices, with Windows' own driver. The camera also stays visible in Explorer. | Built and unit-tested in CI; **not yet tested on a camera**. Reports welcome. |
| Linux | The kernel's USB device interface (usbfs). Unmount the camera if your desktop auto-mounts it, and give your user access to USB vendor `04cb` (udev rule) or use `sudo`. Only one program can hold the camera at a time. | Built and unit-tested in CI; **not yet tested on a camera**. Reports welcome. |

While an `x100vi` command runs, do not use other camera programs (X RAW Studio, X Acquire, Lightroom,
Capture One, Image Capture imports): another program's images could be mistaken for a preview. A second
`x100vi` command waits for the first.

On Windows a request that hangs inside the driver is not timed out by the tool: if a command stops
responding, switch the camera off and on.

If you once replaced the camera's Windows driver with WinUSB (for example with Zadig), switch it back in
Device Manager: uninstall the camera device with "Attempt to remove the driver for this device" (or
"Delete the driver software for this device") ticked, then unplug and replug the camera.

## Safety

- **No firmware operations.** The tool has no code that updates or modifies firmware.
- **Checks before it acts**: wrong camera model, wrong USB mode, another program or another `x100vi`
  command using the camera, an invalid recipe, a RAF from another body, a JPEG instead of a RAF. Each stops
  with an explanation and a fix.
- **Writes are verified.** After saving a recipe the whole custom setting is read back; if the camera holds
  anything else, or changed a setting the recipe did not mention, the command fails and says what differs.
- **Your recipes are not overwritten silently.** `set` refuses an occupied custom setting unless you pass
  `--overwrite`, and saves the previous contents first. `restore` needs `--yes` and backs up first.
  Automatic backups live in `~/.local/state/x100vi/backups` (Windows: `%LOCALAPPDATA%\x100vi\backups`).
- **Full settings files are checked before they are restored**: they must come from an X100VI, match the
  size the camera's firmware uses, and come from the same body (`--other-body` overrides the last check).
  The camera's current file is saved first and the restore is read back. Keep these files private: they
  contain the camera's ID and network settings.
- **Untested firmware** produces a warning, not a refusal; verification still applies.
- A RAW conversion that never finishes can leave the camera unresponsive until it is switched off and on.
  The tool never starts another conversion after one timed out, and tells you when to power-cycle.

## Troubleshooting

`x100vi doctor` names the problem and the fix. The full list of errors and exit codes is in
[skills/x100vi/references/troubleshooting.md](skills/x100vi/references/troubleshooting.md).

## Development

The tool is written in Go. You need the Go version in `go.mod`; on macOS also the Xcode command-line tools
(the ImageCaptureCore bridge uses cgo).

```bash
git clone https://github.com/volodymyrsmirnov/x100vi-skill
cd x100vi-skill
go test ./...          # tests against a software camera; no hardware needed
go build -o x100vi ./cmd/x100vi
```

Tests against a real camera are opt-in and restore what they change:

```bash
X100VI_HARDWARE=1 X100VI_TEST_RAF=/path/to/shot.RAF go test ./internal/hardware -v
```

To try the skill from a checkout, point its launcher at your build, then install the skill from the local
folder:

```bash
export X100VI_BIN="$PWD/x100vi"
```

```bash
npx skills add ./ -g -a claude-code -a codex -y
```

`claude --plugin-dir .` loads it as a plugin for one Claude Code session instead.

How the camera is addressed, the measured property map and the evidence behind each value are in
[docs/protocol.md](docs/protocol.md). Contributor notes: [AGENTS.md](AGENTS.md).

## Credits

- The RAW-conversion sequence and profile layout follow the public notes in
  [zgrimz/fujifilm-ptp-reference](https://github.com/zgrimz/fujifilm-ptp-reference).
- Settings in output files are checked with [ExifTool](https://exiftool.org) by Phil Harvey.

## Disclaimer

This project isn't affiliated with FUJIFILM Corporation. FUJIFILM, X RAW Studio, and camera model names are
trademarks of FUJIFILM Corporation, used here only to identify the hardware described.

Use it at your own risk.

## License

[MIT](LICENSE)
