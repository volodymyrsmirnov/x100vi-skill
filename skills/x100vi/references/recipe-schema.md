# Recipe JSON reference

`x100vi schema` prints the machine-readable JSON Schema. `x100vi validate FILE` is the authority on
whether a recipe is acceptable; it explains every rejection.

## Example

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
  "noise_reduction": -4,
  "clarity": 0
}
```

## Fields

| Key | Values | Default | Rule |
|---|---|---|---|
| `name` | text, up to 25 plain ASCII characters | `""` | shown on the camera for C1-C7 |
| `description`, `notes`, `source`, `tags` | text / list | | never sent to the camera |
| `film_simulation` | `provia`, `velvia`, `astia`, `classic_chrome`, `reala_ace`, `pro_neg_hi`, `pro_neg_std`, `classic_neg`, `nostalgic_neg`, `eterna`, `eterna_bleach_bypass`, `acros`, `acros_ye`, `acros_r`, `acros_g`, `monochrome`, `monochrome_ye`, `monochrome_r`, `monochrome_g`, `sepia` | `provia` | |
| `monochromatic.warm_cool`, `monochromatic.magenta_green` | -18 to 18 | 0 | only with `acros*` or `monochrome*` |
| `grain.strength` | `off`, `weak`, `strong` | `off` | |
| `grain.size` | `small`, `large` | `small` | |
| `color_chrome_effect` | `off`, `weak`, `strong` | `off` | |
| `color_chrome_fx_blue` | `off`, `weak`, `strong` | `off` | |
| `smooth_skin` | `off`, `weak`, `strong` | `off` | |
| `white_balance.mode` | `auto`, `auto_white_priority`, `auto_ambience_priority`, `daylight`, `shade`, `fluorescent_1`, `fluorescent_2`, `fluorescent_3`, `incandescent`, `underwater`, `kelvin`, `custom_1`, `custom_2`, `custom_3` | `auto` | |
| `white_balance.kelvin` | 2500 to 10000, steps of 10 | | required with mode `kelvin`, not allowed otherwise |
| `white_balance.shift_r`, `white_balance.shift_b` | -9 to 9 | 0 | red and blue white-balance shift |
| `dynamic_range` | `auto`, `100`, `200`, `400` | `100` | DR200 needs ISO 250+, DR400 needs ISO 500+ |
| `d_range_priority` | `off`, `weak`, `strong`, `auto` | `off` | when on, the camera ignores `dynamic_range`, `highlight`, `shadow` |
| `highlight`, `shadow` | -2 to 4, steps of 0.5 | 0 | tone curve; positive is harder / darker shadows |
| `color` | -4 to 4 | 0 | not allowed with `acros*`, `monochrome*`, `sepia` |
| `sharpness` | -4 to 4 | 0 | |
| `noise_reduction` | -4 to 4 | 0 | "High ISO NR" |
| `clarity` | -5 to 5 | 0 | non-zero adds about a second after each shot |
| `image_quality` | `raw`, `fine`, `normal`, `fine_raw`, `normal_raw` | untouched | optional; written only when present |
| `image_size` | `l_3:2`, `l_16:9`, `l_1:1`, `l_4:3`, `l_5:4`, and the same for `m_` and `s_` | untouched | optional; not with `raw` |
| `lens_modulation_optimizer` | `on`, `off` | untouched | optional |
| `color_space` | `srgb`, `adobe_rgb` | untouched | optional |

Look settings that are missing are reset to their default when the recipe is saved, so the same file
always gives the same look. The four optional settings at the bottom are left as the camera has them
unless the recipe names them: leave them out of recipes meant to be shared.

## Cannot be set over USB

ISO and auto-ISO limits, exposure compensation, aperture, shutter, focus mode and AF settings, drive mode,
metering, flash, and anything in the set-up menu. Put what matters in `notes` and tell the user to set it
on the body.

## Translating recipe pages

Recipe sites and apps list settings in the camera's menu wording. Map them like this:

| On the page | In the recipe |
|---|---|
| Film Simulation: Classic Chrome / Reala Ace / Nostalgic Neg. / Classic Neg. / PRO Neg. Std / Eterna Bleach Bypass | `classic_chrome` / `reala_ace` / `nostalgic_neg` / `classic_neg` / `pro_neg_std` / `eterna_bleach_bypass` |
| Film Simulation: Acros+R, Monochrome+G | `acros_r`, `monochrome_g` (`_ye` for yellow) |
| Dynamic Range: DR400 | `"dynamic_range": "400"` |
| D-Range Priority: Weak | `"d_range_priority": "weak"` |
| Grain Effect: Weak, Small | `"grain": {"strength": "weak", "size": "small"}` |
| Color Chrome Effect: Strong | `"color_chrome_effect": "strong"` |
| Color Chrome FX Blue: Weak | `"color_chrome_fx_blue": "weak"` |
| White Balance: 5000K, 0 Red & -2 Blue | `"white_balance": {"mode": "kelvin", "kelvin": 5000, "shift_r": 0, "shift_b": -2}` |
| White Balance: Daylight, +3 Red & -5 Blue | `"white_balance": {"mode": "daylight", "shift_r": 3, "shift_b": -5}` |
| White Balance: Auto White Priority / Auto Ambience Priority | `auto_white_priority` / `auto_ambience_priority` |
| White Balance: Fluorescent 1 / 2 / 3 | `fluorescent_1` / `fluorescent_2` / `fluorescent_3` |
| Highlight: -1.5, Shadow: +1.5 | `"highlight": -1.5, "shadow": 1.5` |
| Color: +2 | `"color": 2` |
| Sharpness: -2 | `"sharpness": -2` |
| High ISO NR: -4 (or "Noise Reduction") | `"noise_reduction": -4` |
| Clarity: -3 | `"clarity": -3` |
| Monochromatic Color: WC +2 & MG -1 | `"monochromatic": {"warm_cool": 2, "magenta_green": -1}` |
| Smooth Skin Effect: Weak | `"smooth_skin": "weak"` |
| ISO: Auto, up to ISO 6400 | `notes` (set on the body) |
| Exposure Compensation: +1/3 to +1 | `notes` (set on the body); for previews use `--ev` |

Recipes written for "X-Trans V" or for the X100VI, X-T5, X-H2, X-S20, X-T50, X-E5 or X-M5 use settings
this camera has. Recipes for older sensors may list a film simulation or range the X100VI handles
differently; `validate` will reject anything out of range, and the page usually says how to adapt.
