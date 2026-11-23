# How the X100VI is addressed

Everything here was measured on an X100VI running firmware 1.31, over USB, in
**CONNECTION MODE = USB RAW CONV./BACKUP RESTORE**. The RAW-conversion sequence and profile layout come
from [zgrimz/fujifilm-ptp-reference](https://github.com/zgrimz/fujifilm-ptp-reference); the X100VI
specifics were confirmed here. Codes and value tables were also cross-checked against FUJIFILM X RAW STUDIO
1.28 for macOS, by reading its files without running it; every code and value below agrees with it.

## Transport

- USB still-image class (PTP), vendor `0x04CB`. The X100VI has no tether-shooting mode. In USB CARD READER
  mode it exposes four properties and none of the settings.
- `GetDevicePropDesc` answers GeneralError for every property in this mode, so libgphoto2 cannot build a
  configuration tree. Value ranges were found by asking the camera to validate values and restoring the
  original afterwards.
- All setting values are 16-bit; negative numbers are two's complement.
- **Connections.** The same PTP requests travel three ways, as in FUJIFILM X RAW STUDIO:
  - macOS: ImageCaptureCore. `ptpcamerad` keeps the camera and its PTP session; `ICCameraDevice`
    `requestSendPTPCommand:outData:completion:` takes a full PTP command container plus the raw outgoing
    data and returns the incoming data and the response container. Tested on hardware.
  - Windows: Windows Portable Devices with the inbox driver. `IPortableDevice::SendCommand` with the MTP
    vendor-extension commands (category `{4D545058-1A2E-4106-A357-771E0819FC56}`: 12 no data, 13 data to
    read, 14 data to write, 15 read, 16 write, 17 end transfer; properties 1001 operation code, 1002
    parameters, 1003 response code, 1006 transfer context, 1007 total size, 1008 / 1010 bytes to read /
    write, 1012 data, 1013 optimal buffer size). Not tested on hardware.
  - Any system: libusb, claiming the still-image interface directly. That interface can be held by one
    process only, so on macOS `ptpcamerad` has to be stopped first, and on Windows the WinUSB driver must
    replace the inbox one.
- **Never download an object before describing it.** `GetObject` on handle 0 (the settings file) sent
  without a preceding `GetObjectInfo` hung the X100VI until its battery was removed. The session layer
  refuses that order.

## Properties

| Setting | Live | C1-C7 | Wire values |
|---|---|---|---|
| Register selector | | `0xD18C` | 1-7 |
| Register name | | `0xD18D` | PTP string, up to 25 characters |
| Image size | `0xD1A5` | `0xD18E` | 1-3 S, 4-6 M, 7-9 L (3:2, 16:9, 1:1); 10 S 4:3, 12 S 5:4, 14 L 4:3, 16 L 5:4, 18 M 4:3, 20 M 5:4 |
| Image quality | `0xD018` | `0xD18F` | 1 RAW, 2 FINE, 3 NORMAL, 4 FINE+RAW, 5 NORMAL+RAW |
| Dynamic range | `0xD007` | `0xD190` | 100, 200, 400, `0xFFFF` auto |
| D-range priority | `0xD02E` | `0xD191` | 0 off, 1 weak, 2 strong, `0x8000` auto |
| Film simulation | `0xD001` | `0xD192` | 1-20 (see `model.py`) |
| Mono warm/cool | `0xD104` | `0xD193` | -180 to 180, step 10 |
| Mono magenta/green | `0xD031` | `0xD194` | -180 to 180, step 10 |
| Grain | `0xD023` | `0xD195` | written 1 off, 2 weak/small, 3 strong/small, 4 weak/large, 5 strong/large; reads 2-5, and 6 / 7 for off small / large |
| Colour chrome effect | `0xD029` | `0xD196` | 1 off, 2 weak, 3 strong |
| Colour chrome FX blue | `0xD030` | `0xD197` | 1 off, 2 weak, 3 strong |
| Smooth skin | `0xD189` | `0xD198` | 1 off, 2 weak, 3 strong |
| White balance | `0x5005` | `0xD199` | 2 auto, 4 daylight, 6 incandescent, 8 underwater, `0x8001-3` fluorescent 1-3, `0x8006` shade, `0x8007` Kelvin, `0x8008-A` custom 1-3, `0x8020` auto white priority, `0x8021` auto ambience priority |
| WB shift red / blue | `0xD00B` / `0xD00C` | `0xD19A` / `0xD19B` | -9 to 9 |
| Colour temperature | `0xD017` | `0xD19C` | 2500 to 10000, step 10 |
| Highlight / shadow | `0xD320` / `0xD321` | `0xD19D` / `0xD19E` | -20 to 40, step 5 (value x 10) |
| Colour | `0xD008` | `0xD19F` | -40 to 40, step 10 |
| Sharpness | `0x5015` | `0xD1A0` | -40 to 40, step 10 |
| Noise reduction | `0xD01C` | `0xD1A1` | `0x8000` -4, `0x7000` -3, `0x4000` -2, `0x3000` -1, `0x2000` 0, `0x1000` +1, `0x0000` +2, `0x6000` +3, `0x5000` +4 |
| Clarity | `0xD032` | `0xD1A2` | -50 to 50, step 10 |
| Lens modulation optimizer | `0xD34D` | `0xD1A3` | 1 on, 2 off |
| Colour space | `0xD00A` | `0xD1A4` | 1 sRGB, 2 Adobe RGB |
| Battery | `0xD36B` | | string, first field is percent |

The custom-register block is `0xD18C` (selector), `0xD18D` (name) and the 23 settings `0xD18E`-`0xD1A4`.
`0xD1A5`, which sits right after it, is the **live** image size, not a register setting. It accepts the
image-size codes only while live image quality is not RAW.

## Exposed by the camera but not used

In this mode the X100VI advertises 61 properties and 20 operations. The client uses 52 of the properties:
the 51 that carry settings, names, the conversion and the battery, plus the USB-mode indicator. That
indicator and the nine unused ones:

| Code | What it is | Status |
|---|---|---|
| `0xD16E` | USB mode, read-only: 6 in this mode (5 tether, 8 webcam on other bodies) | read to confirm the mode |
| `0xD184` | Conversion profile codes, `FF179503,FA179503` | understood; informational |
| `0xD186`, `0xD187` | Model key `X100VI_0100`, which X RAW Studio uses to match saved profiles | understood; informational |
| `0xD212` | Event list; signals when a conversion has finished | understood; the client polls object handles instead |
| `0xD36A` | Battery, coarse level (10, later 9, as the percentage fell from 43 to 40) | `0xD36B` is used instead |
| `0xD20B` | Device name, an empty string here | understood; not needed |
| `0xD17B` | Power control: writing 1 switches the camera off (answers GeneralError to a read) | never written |
| `0xD208` | Release (shutter) mode for tethered shooting; reads `0x0304` | not usable in this mode |
| `0xD21C` | Device error status, read after a failed command; reads 1 | not needed |

Film-simulation value `0x8000` is accepted by the registers and rejected by the live property; its meaning
is unknown and the client never writes it.

Operations not used: `0x1017` ResetDevicePropValue and `0x901D`, a second vendor upload.

Conversion profile cells not used: 1 (shooting condition), 28 (digital tele-converter: 1 on, 2 off) and
29 (portrait enhancer, host-write only; not on the X100VI).

## Settings file (backup and restore)

The other half of this mode's name. The camera serves its complete configuration (every menu setting plus
C1-C7) as PTP object handle 0, the same file Fujifilm's backup tools save:

1. `GetObjectInfo` `0x1008` (handle 0): a 56-byte ObjectInfo, format `0x5000`, size 70,524 bytes on
   firmware 1.31.
2. `GetObject` `0x1009` (handle 0): the file. It starts with `FUJIFILMX-BACKUP`, then a 4-character version
   (`0100`), the model name (32 bytes from offset `0x14`) and a 20-character body ID at `0x34` that
   contains the start of the USB serial number. Two reads in a row are byte-identical.
3. Restore: `SendObjectInfo` `0x100C` with parameters (0, 0) and a 1076-byte ObjectInfo that is all zero
   except format `0x5000` and the size, then `SendObject` `0x100D` with parameter 0 and the file.

Restoring the file that was just read takes about a second. Afterwards C1-C7 and the live settings are
unchanged, and the file reads back identical except for two bytes the camera keeps for itself: a 16-bit
counter at `0x108` that went up by 2, and a flag at `0x716` that went from 1 to 3. Neither changed during
20 minutes of reads and conversions. Verification ignores those bytes.

The file is not compressed or encrypted, and it includes network settings, so the tool treats it as
private and opaque: `x100vi restore` checks the header, the model, the size and the body ID, and never
edits the contents.

In USB CARD READER mode the camera advertises only `0x5001` (battery), `0xD303`, `0xD406` and `0xD407`; the
last three are unknown.

## Behaviour the client depends on

- Write the selector before any register property. Right after the camera enters this mode the register
  properties show the live settings, and a write then copies them into the register.
- Naming a factory-blank register for the first time was seen to seed its other settings from the live
  ones. The client writes the name first, then every setting, then verifies the whole register.
- Grain Off is written as 1 and keeps the previous size, so the client sets a size first.
- Changing the Kelvin value zeroes the WB shift; changing WB mode recalls that mode's own shift.
- While D-range priority is on, writes to dynamic range, highlight and shadow are rejected.
- `color` is rejected for monochrome, acros and sepia. Monochromatic tint is rejected for colour sims but
  stays stored; a full recipe for a colour sim clears it by passing through Acros.
- Image size is rejected while image quality is RAW only.
- The card is not browsable in this mode, and there is no capture.

## RAW conversion

1. `0x900C` with an ObjectInfo dataset: format `0xF802`, size, filename `FUP_FILE.dat`.
2. `0x900D` with the RAF bytes.
3. Read `0xD185`: the profile for the uploaded frame, 625 bytes on the X100VI, IOPCode `FF179503`. It
   answers GeneralError when the RAF is not from this model.
4. Write `0xD185` back, patched, in the 629-byte host form (30 cells, `numParams` 29, IOPCode length 9).
5. Re-read and compare, then `0xD183 = 0` (half size) or `1` (full resolution).
6. Poll `GetObjectHandles` for a new object, `GetObject`, `DeleteObject`.

Profile cells are signed 32-bit and hold the register wire values. The SDK's own layout is `numParams`
(u16), the IOPCode string in a fixed `0x1FF`-byte field, then cells from `0x201`. The client counts from
`0x1FD`, so its cell 0 is the last word of the string field (always 0) and its cell *n* is the SDK's cell
*n* - 1:

| Cell | Setting | Cell | Setting | Cell | Setting |
|---|---|---|---|---|---|
| 2 | file type: 7 JPEG, 18 HEIF, 9 / 11 8- / 16-bit TIFF | 12 | WB mode | 21 | lens modulation optimizer |
| 3 | image size | 13, 14 | WB shift red, blue | 22 | colour space |
| 4 | image quality | 15 | Kelvin | 23 | mono warm/cool |
| 5 | exposure, EV x 1000 (-2000 to 3000) | 16, 17 | highlight, shadow | 24 | smooth skin |
| 6 | dynamic range | 18 | colour | 25 | colour chrome FX blue |
| 7 | D-range priority (0, 1, 2, 3) | 19 | sharpness | 26 | mono magenta/green |
| 8 | film simulation | 20 | noise reduction | 27 | clarity |
| 9 | grain (1 = off) | 10 | colour chrome effect | 11 | WB source (1 as shot, 2 profile) |

Limits: dynamic range cannot exceed the frame's as-shot value (a higher one is reported to render with a
green cast); no encoding is known for dynamic range Auto or D-range priority Auto. D-range priority 3 is a
third fixed level (maker notes: fixed, value 3, "Plus" in ExifTool), not Auto; it compresses tones at
least as much as strong. A frame shot with Auto carries the level the camera chose. Exposure outside -2 to
+3 EV is accepted but ignored: -2.33 and -3 rendered identical to 0 EV. The lens modulation optimizer is
honoured when developing (on and off give different pixels).

The file type is honoured as follows: HEIF at either size (format `0x3812`); TIFF only at full resolution
(format `0x380D`, uncompressed RGB, about 125 MB at 8-bit and 245 MB at 16-bit). A TIFF request at half
size returns a JPEG.

## Evidence

- **Ranges**: the camera's own validation of trial writes, each restored afterwards.
- **Labels**: known recipes were loaded into C1-C7 in three rounds, one frame was shot per register, and
  the Fujifilm maker notes (decoded by ExifTool) were compared with the recipes: 393 of 393 tags matched,
  covering all 20 film simulations, all 14 white-balance modes, all 9 noise-reduction codes, all 15 image
  sizes, grain, colour chrome, smooth skin, dynamic range and D-range priority.
- **Conversion**: two frames developed with four recipes; every output's maker notes matched its recipe,
  136 of 136 tags. D-range priority weak and strong were confirmed the same way.
- **Restore**: after all tests the seven registers were byte-identical to a snapshot taken before the
  first write.
- **Settings file**: read twice (identical), restored, and read back (identical apart from the two
  bookkeeping bytes); C1-C7 and the live settings matched before and after.
- **Output formats**: one frame developed as HEIF and as full-size 8- and 16-bit TIFF; each carried the
  Fujifilm maker notes.

`tests/fake_camera.py` reproduces these behaviours in software so the client is tested without hardware;
`tests/hardware/` repeats the essential checks on a real body.

---

This project isn't affiliated with FUJIFILM Corporation. FUJIFILM, X RAW Studio, and camera model names are trademarks of FUJIFILM Corporation, used here only to identify the hardware described.
