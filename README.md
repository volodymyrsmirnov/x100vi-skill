# x100vi-skill

Create film-simulation recipes for a **Fujifilm X100VI** by describing the look you want, see them
rendered on your own photos by the camera's processor, and save the ones you like into the camera's
custom settings (C1-C7), all over a USB cable.

It is two things in one repository:

- **`x100vi`**, a command-line tool that reads, writes and previews recipes on the camera.
- **An agent skill** for [Claude Code](https://code.claude.com) and [Codex](https://github.com/openai/codex)
  that drives the tool for you, so you can say:

  > I'm going to Dubai and want cool photos of deserts. Show me three options on this RAF and put the
  > best one in C3.

**Previews are developed by the camera itself.** The RAF is sent to the camera over USB, the camera's own
image processor converts it to a JPEG with the recipe applied, and the JPEG is sent back. Nothing on the
computer imitates Fujifilm's processing, so what you see is what the camera would have produced.

No firmware is touched. The tool only reads and writes the same camera settings you can change in the
menu, and uses the same in-camera RAW conversion that Fujifilm's X RAW Studio uses.

> [!IMPORTANT]
> **Put the camera in the right USB mode before you connect it.** On the camera:
> **MENU > NETWORK/USB SETTING > CONNECTION MODE > USB RAW CONV./BACKUP RESTORE**, then plug in the cable.
>
> Nothing here works in any other mode, and software cannot change the mode for you. In USB CARD READER
> mode, which is what most cameras are set to, the camera hides all of its settings. See [Set up the camera](#3-set-up-the-camera-required).

## What it can do

| | |
|---|---|
| Read | The current shooting settings and C1-C7, as JSON recipes. Back them all up to one file. |
| Write | A recipe to the live settings or to any of C1-C7, including its name. Every write is read back and compared. |
| Preview | Send a RAF to the camera, where the camera's own processor converts it to JPEG with one or more recipes. You get the JPEGs back plus a contact sheet. The conversion happens entirely on the camera; nothing is stored on it afterwards. |
| Design | With the skill: turn a description, a list of settings, or a recipe web page into a recipe. |

What a recipe controls: film simulation, grain, colour chrome effect, colour chrome FX blue, smooth skin,
white balance (mode, Kelvin, red/blue shift), dynamic range, D-range priority, highlight, shadow, colour,
sharpness, noise reduction, clarity, monochromatic tint, and optionally image quality, image size, colour
space and lens modulation optimizer.

What it cannot control, because the camera does not expose it over USB: ISO, exposure compensation,
aperture, shutter, focus and AF settings, drive mode, metering, flash. Recipes carry these as `notes` for
you to set on the body.

## Requirements

- A Fujifilm **X100VI** (tested on firmware 1.31) and a USB-C **data** cable.
- The camera's CONNECTION MODE set to **USB RAW CONV./BACKUP RESTORE** ([how](#3-set-up-the-camera-required)).
- [uv](https://docs.astral.sh/uv/) (or pipx with Python 3.10 or newer) to install and run the tool.
- Node.js 22.20 or later, only if you install the skill with `npx skills`.
- macOS (tested). Linux should work; Windows needs an extra driver. See [Platforms](#platforms).
- Optional: [exiftool](https://exiftool.org), only for `x100vi verify` and `preview --verify`.

## Install

### 1. The skill

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

**That is all you need.** The skill carries a small launcher that installs the `x100vi` tool by itself the
first time the agent uses it. That step needs [uv](https://docs.astral.sh/uv/getting-started/installation/)
(or pipx) on your machine; if neither is there, the agent tells you what to install.

Other ways to get the skill:

- **Claude Code plugin**: `/plugin marketplace add volodymyrsmirnov/x100vi-skill`, then
  `/plugin install x100vi@x100vi-skill`.
- **From the tool** (if you installed it as in step 2): `x100vi install-skill --claude --codex`, or
  `--dir SKILLS_FOLDER` for any agent that reads `SKILL.md` folders.

### 2. The command-line tool (optional)

Install it yourself if you want to use `x100vi` from a terminal, or prefer not to let the skill install it:

```bash
uv tool install git+https://github.com/volodymyrsmirnov/x100vi-skill
```

```bash
x100vi --version
```

With pipx instead: `pipx install git+https://github.com/volodymyrsmirnov/x100vi-skill`. The skill uses an
installed `x100vi` when it finds one. To update later: `uv tool upgrade x100vi`.

### 3. Set up the camera (required)

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

With the camera in **USB RAW CONV./BACKUP RESTORE** mode and connected ([step 3](#3-set-up-the-camera-required)),
just ask. If the mode is wrong, the agent stops and tells you how to change it. Examples of what the skill
handles:

- "Make me a moody black-and-white street look and show it on `DSCF4158.RAF`."
- "Use this recipe: <link to a recipe page>. Put it in C2 and call it PORTRA."
- "What's in my C1 to C7? Save them to `my-camera.json`."
- "Here are three RAFs from Dubai; show me how they'd look with a warm desert look and a muted one."

It asks before writing to the camera, never replaces a custom setting that already holds a recipe without
your say-so, and tells you when a preview could not show a setting because of how the photo was taken.

## Using the tool directly

Every command that talks to the camera (`doctor`, `get`, `set`, `backup`, `restore`, `preview`) needs it
connected in **USB RAW CONV./BACKUP RESTORE** mode. `recipes`, `validate` and `schema` work without a camera.

```bash
x100vi recipes                               # bundled starter recipes
x100vi validate my-look.json                 # check a recipe, no camera needed
x100vi get all                               # live settings and C1-C7 as JSON
x100vi get C3 -o c3.json                     # one custom setting to a file
x100vi set C3 my-look.json                   # save a recipe (refuses if C3 is occupied)
x100vi set C3 my-look.json --overwrite       # replace it; the old one is saved first
x100vi set live desert-gold                  # apply a starter recipe to the current settings
x100vi backup -o my-camera.json              # everything in one file
x100vi restore my-camera.json --yes          # write C1-C7 back
x100vi preview shot.RAF --as-shot -r desert-gold street-mono my-look.json
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

- **The RAW-to-JPEG conversion runs on the camera, not on the computer.** The tool uploads the RAF, tells
  the camera which settings to use, waits while the camera develops it, and downloads the result. That is
  why the camera must be connected and switched on, why it uses the camera's battery, and why the output
  matches an in-camera JPEG.
- The RAF must come from an X100VI. It is uploaded once and developed once per recipe.
- Output is half size by default; `--full` gives full resolution. `--ev 0.67` adds exposure compensation.
- A preview shows colour and tone on a frame you already shot. It cannot show what a different ISO or
  exposure at capture would have done.

## Safety

- **No firmware operations.** The tool has no code that updates or modifies firmware.
- **Checks before it acts**: wrong camera model, wrong USB mode, another program holding the camera, an
  invalid recipe, a RAF from another body, a JPEG instead of a RAF. Each stops with an explanation and a fix.
- **Writes are verified.** After saving a recipe the whole custom setting is read back; if the camera
  holds anything else, the command fails and says what differs.
- **Your recipes are not overwritten silently.** `set` refuses an occupied custom setting unless you pass
  `--overwrite`, and saves the previous contents first. `restore` needs `--yes` and backs up first.
  Automatic backups live in `~/.local/state/x100vi/backups`.
- **Untested firmware** produces a warning, not a refusal; verification still applies.
- A RAW conversion that never finishes can leave the camera unresponsive until it is switched off and
  on. The tool never retries one on its own and tells you when to power-cycle.

## Platforms

| | Status |
|---|---|
| macOS | Tested on hardware. Works out of the box. |
| Linux | Unit-tested in CI, not tested on hardware. Unmount the camera if your desktop auto-mounts it, and give your user access to USB vendor `04cb` (udev rule) or use `sudo`. |
| Windows | Unit-tested in CI, **not tested on hardware**. libusb reaches the camera only through the WinUSB driver: install it for the camera with [Zadig](https://zadig.akeo.ie). While it is installed, Explorer and X RAW STUDIO cannot see the camera; uninstall the driver in Device Manager to switch back. Reports welcome. |

Only one program can hold the camera: quit X RAW STUDIO, X Acquire, Image Capture, Photos and Lightroom first.

## Troubleshooting

`x100vi doctor` names the problem and the fix. The full list of errors and exit codes is in
[skills/x100vi/references/troubleshooting.md](skills/x100vi/references/troubleshooting.md).

## Development

```bash
git clone https://github.com/volodymyrsmirnov/x100vi-skill
cd x100vi-skill
uv sync
uv run pytest          # about 180 tests against a software camera; no hardware needed
uv run ruff check
```

Tests against a real camera are opt-in and restore what they change:

```bash
X100VI_HARDWARE=1 X100VI_TEST_RAF=/path/to/shot.RAF uv run pytest tests/hardware -v
```

To try the skill from a checkout, install the tool from it first so the skill's launcher finds it, then
install the skill from the local folder:

```bash
uv tool install --editable .
```

```bash
npx skills add ./ -g -a claude-code -a codex -y
```

`claude --plugin-dir .` loads it as a plugin for one Claude Code session instead. The launcher's install
source can be pointed at a checkout with `X100VI_SOURCE=/path/to/x100vi-skill`.

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
