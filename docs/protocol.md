# How the X100VI is addressed

Everything here was measured on an X100VI running firmware 1.31, over USB, in
**CONNECTION MODE = USB RAW CONV./BACKUP RESTORE**. The RAW-conversion sequence and profile layout come
from [zgrimz/fujifilm-ptp-reference](https://github.com/zgrimz/fujifilm-ptp-reference); the X100VI
specifics were confirmed here.

## Transport

- USB still-image class (PTP), vendor `0x04CB`. The X100VI has no tether-shooting mode. In USB CARD READER
  mode it exposes four properties and none of the settings.
- `GetDevicePropDesc` answers GeneralError for every property in this mode, so libgphoto2 cannot build a
  configuration tree. Value ranges were found by asking the camera to validate values and restoring the
  original afterwards.
- All setting values are 16-bit; negative numbers are two's complement.

## Properties

| Setting | Live | C1-C7 | Wire values |
|---|---|---|---|
| Register selector | | `0xD18C` | 1-7 |
| Register name | | `0xD18D` | PTP string, up to 25 characters |
| Image size | `0xD16E` | `0xD18E` | 1-3 S, 4-6 M, 7-9 L (3:2, 16:9, 1:1); 10 S 4:3, 12 S 5:4, 14 L 4:3, 16 L 5:4, 18 M 4:3, 20 M 5:4 |
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

Not identified: register property `0xD1A5` (read-only, 7) and film-simulation value `0x8000`, which the
registers accept and the live property rejects.

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

Profile cells are signed 32-bit at fixed offset `0x1FD` and hold the register wire values:

| Cell | Setting | Cell | Setting | Cell | Setting |
|---|---|---|---|---|---|
| 2 | file type (7 JPEG) | 12 | WB mode | 21 | lens modulation optimizer |
| 3 | image size | 13, 14 | WB shift red, blue | 22 | colour space |
| 4 | image quality | 15 | Kelvin | 23 | mono warm/cool |
| 5 | exposure, EV x 1000 | 16, 17 | highlight, shadow | 24 | smooth skin |
| 6 | dynamic range | 18 | colour | 25 | colour chrome FX blue |
| 7 | D-range priority (0, 1, 2) | 19 | sharpness | 26 | mono magenta/green |
| 8 | film simulation | 20 | noise reduction | 27 | clarity |
| 9 | grain (1 = off) | 10 | colour chrome effect | 11 | WB source (1 as shot, 2 profile) |

Limits: dynamic range cannot exceed the frame's as-shot value (a higher one is reported to render with a
green cast); no encoding is known for dynamic range Auto or D-range priority Auto.

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

`tests/fake_camera.py` reproduces these behaviours in software so the client is tested without hardware;
`tests/hardware/` repeats the essential checks on a real body.
