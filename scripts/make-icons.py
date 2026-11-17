#!/usr/bin/env python3
"""Regenerate the Play feature graphic from the Aether mark and wordmark.

    python3 scripts/make-icons.py

Writes android/listing/feature-graphic.png. Needs `pillow`, and
`fonttools[woff]` to read the dashboard's own woff2 type: the extra is brotli,
which the plain fontTools install leaves out and woff2 cannot be decoded
without. The graphic still shows the Aether name until the owner replaces it.
The desktop and dashboard icons are the Cloudfield logo set's files
(CONTRIBUTING.md); the Android app's come from scripts/android-icons.py.
"""

import io
from pathlib import Path

from fontTools.ttLib import TTFont
from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "web" / "public" / "aether-mark.png"
FONTS = ROOT / "web" / "public" / "fonts"
ANDROID_LISTING = ROOT / "android" / "listing"

# Google Play's feature graphic is a fixed 1024x500 banner in JPEG or 24-bit
# PNG with no alpha (android/listing/README.md). Play's highly-recommended
# rules also rule out a banner that repeats the launcher icon or sits on
# black, white or dark grey - either costs the listing its place in the
# promotional formats - so this one is the wordmark and one line of copy on
# the product blue, with the mark small and secondary. Play crops it on some
# surfaces, so everything stays in the middle with wide margins.
FEATURE_SIZE = (1024, 500)

# Top-left and bottom-right of the ground, both shades of the dashboard's
# --primary. White on the lighter end is 7.9:1.
FEATURE_GROUND = ((10, 82, 150), (7, 60, 114))
FEATURE_WORDMARK = "aether"
FEATURE_TAGLINE = "Your coding agents, on your own server."
FEATURE_WORDMARK_PX = 208
FEATURE_TAGLINE_PX = 34
FEATURE_MARK_PX = 108
FEATURE_GAP_PX = 34
FEATURE_TAGLINE_GAP_PX = 54
FEATURE_WORDMARK_INK = (255, 255, 255)
FEATURE_TAGLINE_INK = (234, 242, 251)

# Reading both from web/public/fonts keeps the banner reproducible without a
# system font or a second copy in the tree.
WORDMARK_FONT = FONTS / "vt323-latin.woff2"
TAGLINE_FONT = FONTS / "jetbrains-mono-nfm-regular.woff2"


def load_mark() -> Image.Image:
    """The mark, cropped to its ink so padding is computed here, not inherited."""
    mark = Image.open(SOURCE).convert("RGBA")
    return mark.crop(mark.getbbox())


def load_font(path: Path, size: int) -> ImageFont.FreeTypeFont:
    """Pillow cannot read woff2, so the dashboard's font file is decompressed
    to a TTF in memory rather than duplicated in the repository."""
    font = TTFont(path)
    font.flavor = None
    buffer = io.BytesIO()
    font.save(buffer)
    buffer.seek(0)
    return ImageFont.truetype(buffer, size)


def write_listing(mark: Image.Image) -> None:
    ANDROID_LISTING.mkdir(parents=True, exist_ok=True)
    width, height = FEATURE_SIZE
    corner, opposite = FEATURE_GROUND
    middle = tuple((a + b) // 2 for a, b in zip(corner, opposite))
    ground = Image.new("RGB", (2, 2))
    ground.putdata([corner, middle, middle, opposite])
    banner = ground.resize(FEATURE_SIZE, Image.BILINEAR)

    wordmark = load_font(WORDMARK_FONT, FEATURE_WORDMARK_PX)
    tagline = load_font(TAGLINE_FONT, FEATURE_TAGLINE_PX)
    draw = ImageDraw.Draw(banner)

    # Both boxes are ink, not line boxes, so the block centers on what is seen.
    word_box = draw.textbbox((0, 0), FEATURE_WORDMARK, font=wordmark)
    tag_box = draw.textbbox((0, 0), FEATURE_TAGLINE, font=tagline)
    word_width, word_height = word_box[2] - word_box[0], word_box[3] - word_box[1]
    mark_width = round(mark.width * (FEATURE_MARK_PX / mark.height))
    scaled = mark.resize((mark_width, FEATURE_MARK_PX), Image.LANCZOS)

    row_height = max(FEATURE_MARK_PX, word_height)
    block = row_height + FEATURE_TAGLINE_GAP_PX + tag_box[3] - tag_box[1]
    top = (height - block) // 2
    left = (width - (mark_width + FEATURE_GAP_PX + word_width)) // 2

    banner.paste(scaled, (left, top + (row_height - FEATURE_MARK_PX) // 2), scaled)
    draw.text(
        (
            left + mark_width + FEATURE_GAP_PX - word_box[0],
            top + (row_height - word_height) // 2 - word_box[1],
        ),
        FEATURE_WORDMARK,
        font=wordmark,
        fill=FEATURE_WORDMARK_INK,
    )
    draw.text(
        (
            (width - (tag_box[2] - tag_box[0])) // 2 - tag_box[0],
            top + row_height + FEATURE_TAGLINE_GAP_PX - tag_box[1],
        ),
        FEATURE_TAGLINE,
        font=tagline,
        fill=FEATURE_TAGLINE_INK,
    )

    banner.save(ANDROID_LISTING / "feature-graphic.png")
    print(f"wrote {ANDROID_LISTING.relative_to(ROOT)}/feature-graphic.png")


def main() -> None:
    write_listing(load_mark())


if __name__ == "__main__":
    main()
