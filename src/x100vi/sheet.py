"""Contact sheet: one row per RAF, one labelled column per render, for comparing looks at a glance."""
from __future__ import annotations

from pathlib import Path

from PIL import Image, ImageDraw, ImageFont, ImageOps

CELL_LONG, PAD, LABEL = 720, 16, 44
BACKGROUND, TEXT = (24, 24, 24), (235, 235, 235)
FONT_CANDIDATES = (
    "/System/Library/Fonts/Helvetica.ttc",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
    "/usr/share/fonts/dejavu/DejaVuSans.ttf",
    "C:/Windows/Fonts/arial.ttf",
)


def _font(size: int):
    for path in FONT_CANDIDATES:
        try:
            return ImageFont.truetype(path, size)
        except OSError:
            continue
    return ImageFont.load_default()


def contact_sheet(rows: list[list[tuple[str, Path]]], out: Path) -> Path:
    """rows[r] is a list of (label, image path). Each image keeps its own orientation and aspect."""
    if not rows or not any(rows):
        raise ValueError("nothing to put on the sheet")
    font = _font(26)
    thumbs: list[list[tuple[str, Image.Image]]] = []
    for row in rows:
        line = []
        for label, path in row:
            with Image.open(path) as im:
                im = ImageOps.exif_transpose(im).convert("RGB")
                im.thumbnail((CELL_LONG, CELL_LONG), Image.LANCZOS)
                line.append((label, im.copy()))
        thumbs.append(line)
    col_w = max(im.width for line in thumbs for _, im in line)
    row_h = [max(im.height for _, im in line) for line in thumbs]
    cols = max(len(line) for line in thumbs)
    sheet = Image.new("RGB", (PAD + cols * (col_w + PAD), PAD + sum(h + LABEL + PAD for h in row_h)), BACKGROUND)
    draw = ImageDraw.Draw(sheet)
    y = PAD
    for line, h in zip(thumbs, row_h, strict=True):
        for c, (label, im) in enumerate(line):
            x = PAD + c * (col_w + PAD)
            draw.text((x, y + 6), label, fill=TEXT, font=font)
            sheet.paste(im, (x, y + LABEL))
        y += h + LABEL + PAD
    out.parent.mkdir(parents=True, exist_ok=True)
    sheet.save(out, quality=90)
    return out
