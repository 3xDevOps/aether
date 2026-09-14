#!/usr/bin/env python3
"""Regenerate the desktop, web and Android app icons from the Aether mark.

Run after web/public/aether-mark.png changes:

    python3 scripts/make-icons.py

Writes desktop/build/icons/ and desktop/build/icon.ico, which electron-builder
resolves for every platform, converting the set to .icns for macOS; only
Windows needs a prebuilt file. Writes web/public/icons/ for the web app
manifest and the iOS home screen, android/app/src/main/res/mipmap-* for the
phone app's launcher icon, and android/listing/feature-graphic.png for the
Play listing. Needs Pillow, and fontTools to read the dashboard's own woff2
type for the Play banner.

The mark is thin light-blue line art on transparency, so it is composited onto
a tile in the dashboard's --background color rather than shipped bare:
transparent line art disappears against a light desktop, taskbar or home
screen. Small rasters give the mark proportionally more of the tile, because
holding the large-icon padding at 16px thins the strokes below one pixel and
the shape turns to mush.

Two tile shapes ship. The desktop set and the manifest's `any` icons are
rounded, which is what a desktop, a taskbar or a launcher that applies no mask
of its own expects. The maskable icons and the iOS home-screen icon are square
and full-bleed, because those platforms apply their own mask: a rounded tile
inside one reads as a tile with clipped corners.
"""

import io
from pathlib import Path

from fontTools.ttLib import TTFont
from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parents[1]
SOURCE = ROOT / "web" / "public" / "aether-mark.png"
FONTS = ROOT / "web" / "public" / "fonts"
DESKTOP_BUILD = ROOT / "desktop" / "build"
WEB_ICONS = ROOT / "web" / "public" / "icons"
ANDROID_RES = ROOT / "android" / "app" / "src" / "main" / "res"
ANDROID_LISTING = ROOT / "android" / "listing"

# The ground every Aether icon is drawn on, #0a0a0a. The web app manifest's
# `background_color` matches it so the tile does not show as a square on an
# installed app's splash (`web/src/app/theme-color.ts`). It is darker than the
# dashboard's own dark `--background`, #1f1f1f: an icon has to read as an
# object on a launcher rather than blend into a page.
# android/app/src/main/res/values/colors.xml repeats it for the adaptive
# icon's background layer, which Android fills rather than draws.
BACKGROUND = (10, 10, 10, 255)
CORNER_RADIUS_RATIO = 0.2237

# Fraction of the tile's width the mark spans, per output size.
COVERAGE = {16: 0.86, 32: 0.82, 48: 0.78, 64: 0.74, 128: 0.68, 256: 0.64, 512: 0.62, 1024: 0.62}

# Windows shows this icon everywhere from the taskbar to Explorer's detail
# rows, so the .ico carries its own sizes rather than letting Windows
# downscale one large bitmap. 24px has no tuned entry of its own.
ICO_SIZES = [16, 24, 32, 48, 64, 128, 256]

# Chrome will not offer to install a web app whose manifest lacks a 192px and
# a 512px icon, so those two sizes ship in both purposes.
WEB_SIZES = [192, 512]
WEB_COVERAGE = 0.62

# A maskable icon is cropped by the launcher, which guarantees only a circle
# 80% of the icon wide. At this width the mark's half-diagonal is 0.37 of the
# icon - inside that 0.4 radius - so no launcher shape clips it.
MASKABLE_COVERAGE = 0.6

# iOS renders the home-screen icon at 180px on every phone it still ships to
# and rounds the corners itself.
APPLE_SIZE = 180

# Android density buckets and their pixels per dp.
ANDROID_DENSITIES = {"mdpi": 1, "hdpi": 1.5, "xhdpi": 2, "xxhdpi": 3, "xxxhdpi": 4}

# A launcher icon is 48dp; an adaptive icon's layers are a 108dp canvas the
# launcher masks and animates, of which only the central 66dp circle is
# guaranteed to survive every mask. 0.48 puts the mark's diagonal at 63dp,
# just inside that circle.
ANDROID_LEGACY_DP = 48
ANDROID_ADAPTIVE_DP = 108
ANDROID_ADAPTIVE_COVERAGE = 0.48

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

# VT323 carries the `aether` wordmark on the dashboard and JetBrains Mono its
# terminals (web/src/index.css). Reading both from web/public/fonts keeps the
# banner reproducible without a system font or a second copy in the tree.
WORDMARK_FONT = FONTS / "vt323-latin.woff2"
TAGLINE_FONT = FONTS / "jetbrains-mono-nfm-regular.woff2"

# Supersampling factor: render large, then box down, so the rounded corners and
# the mark's diagonals land antialiased.
SUPERSAMPLE = 8


def load_mark() -> Image.Image:
    """The mark, cropped to its ink so padding is computed here, not inherited."""
    mark = Image.open(SOURCE).convert("RGBA")
    return mark.crop(mark.getbbox())


def coverage_for(size: int) -> float:
    """The tuned coverage of the nearest size COVERAGE was hand-checked at."""
    return COVERAGE[min(COVERAGE, key=lambda tuned: abs(tuned - size))]


def paste_mark(mark: Image.Image, tile: Image.Image, coverage: float) -> None:
    width = round(tile.width * coverage)
    height = round(mark.height * (width / mark.width))
    scaled = mark.resize((width, height), Image.LANCZOS)
    tile.alpha_composite(scaled, ((tile.width - width) // 2, (tile.height - height) // 2))


def render(mark: Image.Image, size: int, coverage: float, rounded: bool = True) -> Image.Image:
    supersampled = size * SUPERSAMPLE
    tile = Image.new("RGBA", (supersampled, supersampled), (0, 0, 0, 0))

    background = Image.new("RGBA", (supersampled, supersampled), BACKGROUND)
    if rounded:
        mask = Image.new("L", (supersampled, supersampled), 0)
        ImageDraw.Draw(mask).rounded_rectangle(
            [0, 0, supersampled - 1, supersampled - 1],
            radius=round(supersampled * CORNER_RADIUS_RATIO),
            fill=255,
        )
        tile.paste(background, (0, 0), mask)
    else:
        tile.paste(background, (0, 0))

    paste_mark(mark, tile, coverage)
    return tile.resize((size, size), Image.LANCZOS)


def render_adaptive_foreground(mark: Image.Image, size: int) -> Image.Image:
    """The mark alone on transparency: the adaptive icon's background layer is
    a flat color the launcher parallaxes independently, so no tile here."""
    supersampled = size * SUPERSAMPLE
    layer = Image.new("RGBA", (supersampled, supersampled), (0, 0, 0, 0))
    paste_mark(mark, layer, ANDROID_ADAPTIVE_COVERAGE)
    return layer.resize((size, size), Image.LANCZOS)


def write_desktop(mark: Image.Image) -> None:
    icons = DESKTOP_BUILD / "icons"
    icons.mkdir(parents=True, exist_ok=True)

    rendered = {size: render(mark, size, coverage) for size, coverage in COVERAGE.items()}
    for size, image in rendered.items():
        image.save(icons / f"{size}x{size}.png")

    frames = [
        rendered[size] if size in rendered else render(mark, size, COVERAGE[32])
        for size in ICO_SIZES
    ]
    frames[-1].save(
        DESKTOP_BUILD / "icon.ico",
        format="ICO",
        sizes=[(size, size) for size in ICO_SIZES],
        append_images=frames[:-1],
    )

    print(f"wrote desktop/build/icons/ ({len(rendered)} sizes) and icon.ico ({len(ICO_SIZES)} sizes)")


def write_web(mark: Image.Image) -> None:
    WEB_ICONS.mkdir(parents=True, exist_ok=True)

    for size in WEB_SIZES:
        render(mark, size, WEB_COVERAGE).save(WEB_ICONS / f"icon-{size}.png")
        render(mark, size, MASKABLE_COVERAGE, rounded=False).save(
            WEB_ICONS / f"icon-maskable-{size}.png"
        )
    render(mark, APPLE_SIZE, MASKABLE_COVERAGE, rounded=False).save(
        WEB_ICONS / "apple-touch-icon.png"
    )

    print(f"wrote web/public/icons/ ({2 * len(WEB_SIZES) + 1} files)")


def write_android(mark: Image.Image) -> None:
    """The adaptive foreground per density, plus the square tile launchers that
    do not render adaptive icons fall back to. The two XML layer definitions
    are hand-written resources, not generated."""
    for bucket, scale in ANDROID_DENSITIES.items():
        directory = ANDROID_RES / f"mipmap-{bucket}"
        directory.mkdir(parents=True, exist_ok=True)

        legacy = round(ANDROID_LEGACY_DP * scale)
        render(mark, legacy, coverage_for(legacy)).save(directory / "ic_launcher.png")

        adaptive = round(ANDROID_ADAPTIVE_DP * scale)
        render_adaptive_foreground(mark, adaptive).save(directory / "ic_launcher_foreground.png")

    print(f"wrote {ANDROID_RES.relative_to(ROOT)}/mipmap-* ({len(ANDROID_DENSITIES)} densities)")


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
    mark = load_mark()
    write_desktop(mark)
    write_web(mark)
    write_android(mark)
    write_listing(mark)


if __name__ == "__main__":
    main()
