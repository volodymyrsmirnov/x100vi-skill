# Designing a recipe from a description

A recipe is a starting look, not a guarantee. Light, subject and exposure change the result more than
any single setting, so offer two or three distinct candidates and let a preview decide.

## Method

1. **Read the brief for four things**: subject (landscape, people, street, architecture), light (hard
   midday sun, golden hour, overcast, night, indoor), mood (warm, cool, muted, vivid, nostalgic, clean),
   and colour or black and white. If the brief leaves colour vs. black and white open, include one of each.
2. **Pick the film simulation first.** It sets the character; everything else is adjustment.
3. **Set white balance for the mood**, then tone (`highlight`, `shadow`, `dynamic_range`), then colour
   depth (`color`, `color_chrome_effect`, `color_chrome_fx_blue`), then texture (`grain`, `sharpness`,
   `noise_reduction`, `clarity`).
4. **Make the candidates different on purpose**: for example one natural, one stylised, one
   black and white. Three near-identical recipes help nobody.
5. **Write `notes`** with what must be set on the body: ISO range and exposure compensation.
6. `x100vi validate`, then preview on the user's own RAF if they have one.

`x100vi recipes --json` lists bundled starters with tags (desert, portrait, night, street, ...). Adapting
one is fine; say which one you started from.

## Film simulations

| Value | Character | Reach for it when |
|---|---|---|
| `provia` | neutral, standard contrast and colour | a clean baseline |
| `velvia` | vivid: high saturation and contrast | landscapes, flowers, postcard colour; avoid for skin |
| `astia` | soft contrast, gentle colour | people in daylight, pastel scenes |
| `classic_chrome` | muted colour, firmer shadows, documentary feel | travel, street, dry landscapes, a restrained look |
| `reala_ace` | faithful colour with a little more bite than Provia | everyday, travel where colours should look true |
| `pro_neg_hi` | portrait colour with more contrast | people in flat light |
| `pro_neg_std` | softest gradation, low saturation | portraits, skin, controlled light |
| `classic_neg` | strong contrast, shifted colours, snapshot-film feel | street, night, neon, a nostalgic print look |
| `nostalgic_neg` | amber-tinted highlights, rich saturated shadows | golden hour, warm interiors, a 1970s colour-print feel |
| `eterna` | cinema: low saturation, low contrast, open shadows | moody scenes, video-like stills, harsh light you want tamed |
| `eterna_bleach_bypass` | low saturation, high contrast | gritty, cold, industrial |
| `acros`, `acros_ye`, `acros_r`, `acros_g` | fine-grained black and white with deep blacks | most black and white; filters below |
| `monochrome`, `monochrome_ye`, `monochrome_r`, `monochrome_g` | plainer black and white | a softer, flatter monochrome |
| `sepia` | brown-toned monochrome | rarely; prefer a mono sim with `warm_cool` |

Black-and-white filters act like glass filters on film: `_ye` darkens sky a little; `_r` darkens blue sky
strongly and lightens skin; `_g` lightens foliage and gives fuller skin tones. `color` does not apply to
black and white; tint with `monochromatic.warm_cool` (positive is warm) and `magenta_green`.

## What each setting does

| Setting | Lower | Higher |
|---|---|---|
| `white_balance.kelvin` | cooler, bluer picture | warmer, more amber picture |
| `white_balance.shift_r` | toward cyan | toward red |
| `white_balance.shift_b` | toward yellow | toward blue |
| `highlight` | softer highlights, more detail kept | brighter, harder highlights |
| `shadow` | lifted, open shadows | darker, deeper shadows |
| `color` | muted | saturated |
| `sharpness` | softer, more filmic edges | crisper edges |
| `noise_reduction` | more texture and natural noise (-4 keeps the most detail) | smoother, more smeared at high ISO |
| `clarity` | glow, softened midtones | midtone punch and grit |

- **Warm look**: Kelvin above about 5600, or `shift_r` positive with `shift_b` negative. **Cool look**: the
  opposite. Fixed Kelvin gives a consistent look across a series; `auto` adapts shot to shot.
- **`dynamic_range`** 200 or 400 protects bright highlights (sky, sand, snow, white buildings) at the cost
  of a higher minimum ISO: 250 for DR200, 500 for DR400. In bright sun use 200; with harsh contrast and
  auto-ISO allowed to rise, 400.
- **`d_range_priority`** flattens both ends for extreme contrast, and overrides `dynamic_range`,
  `highlight` and `shadow`. Use it or them, not both. `auto` cannot be previewed.
- **`color_chrome_effect`** deepens strongly saturated colours (reds, oranges, greens) so they keep
  texture. **`color_chrome_fx_blue`** does the same for blues: deeper skies and water.
- **`grain`** adds film-like texture: `weak`/`small` is subtle; `strong`/`large` is obvious even at web size.
- **`clarity`** other than 0 makes the camera pause about a second after each shot. Mention it; prefer 0
  for anything fast-moving.
- **`smooth_skin`** softens skin only; leave `off` outside portraits.

## Starting points by situation

These are defaults to adjust, not rules.

| Situation | Direction |
|---|---|
| Desert, beach, dry landscape in sun | `classic_chrome` or `reala_ace`; warm white balance (daylight with `shift_r` +2..+4, `shift_b` -3..-6, or Kelvin 6000-6500); `dynamic_range` 200-400 for bright sand; `color_chrome_fx_blue` weak or strong for the sky; `highlight` -1; exposure compensation +1/3 to +2/3 because bright sand makes the meter underexpose |
| Golden hour, sunset | `nostalgic_neg` or `classic_chrome`; Kelvin 6000-7000; `highlight` -1 to -2 to keep the sky; `shadow` -1 to 0 |
| City at night, neon | `classic_neg` or `eterna`; Kelvin 3800-4500 for a cool cast; `shadow` +1 to +2; `grain` strong; exposure compensation -1/3 to -1 |
| Street, documentary | `classic_chrome` or `classic_neg`; `auto` white balance; `grain` weak; `sharpness` 0 to -1 |
| Portraits | `pro_neg_std` or `astia`; `highlight` -1, `shadow` -1; `sharpness` -1 to -2; `smooth_skin` off or weak |
| Lush landscape, travel postcards | `velvia`; `color_chrome_effect` strong; `dynamic_range` 200; watch saturated reds |
| Overcast, rain, fog | `eterna` or `classic_chrome`; `shadow` 0 to +1 for some depth; `color` +1 |
| Black and white with drama | `acros_r`; `shadow` +2; `highlight` +1; `grain` strong/large |
| Quiet black and white | `monochrome` or `acros`; `warm_cool` +2..+4; `highlight` -1; `grain` weak |

## Previewing honestly

- A preview develops a frame the user already shot, so it shows colour and tone, not what exposure
  compensation or a different ISO would have done at capture. `--ev` simulates exposure compensation.
- A frame can only be developed up to the dynamic range it was shot at. If the preview reports that
  DR400 was rendered at 200, the recipe will still use 400 in the field when ISO allows.
- Judge candidates on a scene like the one the user will shoot. A night recipe previewed on a noon
  photograph tells you little; say so.
- Never describe a candidate as reproducing a named film stock. Describe what it does to the picture.
