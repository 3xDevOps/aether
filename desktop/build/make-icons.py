#!/usr/bin/env python3
"""Regenerate the desktop app icons from the dashboard's Aether mark.

Run after web/public/aether-mark.png changes:

    python3 desktop/build/make-icons.py

Writes icons/ and icon.ico. electron-builder resolves the icon set from
icons/ for every platform, converting it to .icns for macOS; only Windows
needs a prebuilt file. Needs Pillow.

The mark is thin light-blue line art on transparency, so it is composited onto
a rounded tile in the dashboard's --background color rather than shipped bare:
transparent line art disappears against a light desktop or taskbar. Small
rasters give the mark proportionally more of the tile, because holding the
large-icon padding at 16px thins the strokes below one pixel and the shape
turns to mush.
"""

from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parents[2]
SOURCE = ROOT / "web" / "public" / "aether-mark.png"
BUILD = Path(__file__).resolve().parent

# The dashboard's dark `--background` token, oklch(0.145 0 0), in sRGB. The
# Electron window already uses this hex to avoid a white flash before paint.
BACKGROUND = (10, 10, 10, 255)
CORNER_RADIUS_RATIO = 0.2237

# Fraction of the tile's width the mark spans, per output size.
COVERAGE = {16: 0.86, 32: 0.82, 48: 0.78, 64: 0.74, 128: 0.68, 256: 0.64, 512: 0.62, 1024: 0.62}

# Windows shows this icon everywhere from the taskbar to Explorer's detail
# rows, so the .ico carries its own sizes rather than letting Windows
# downscale one large bitmap. 24px has no tuned entry of its own.
ICO_SIZES = [16, 24, 32, 48, 64, 128, 256]

# Supersampling factor: render large, then box down, so the rounded corners and
# the mark's diagonals land antialiased.
SUPERSAMPLE = 8


def load_mark() -> Image.Image:
    """The mark, cropped to its ink so padding is computed here, not inherited."""
    mark = Image.open(SOURCE).convert("RGBA")
    return mark.crop(mark.getbbox())


def render(mark: Image.Image, size: int, coverage: float) -> Image.Image:
    supersampled = size * SUPERSAMPLE
    tile = Image.new("RGBA", (supersampled, supersampled), (0, 0, 0, 0))

    mask = Image.new("L", (supersampled, supersampled), 0)
    ImageDraw.Draw(mask).rounded_rectangle(
        [0, 0, supersampled - 1, supersampled - 1],
        radius=round(supersampled * CORNER_RADIUS_RATIO),
        fill=255,
    )
    tile.paste(Image.new("RGBA", (supersampled, supersampled), BACKGROUND), (0, 0), mask)

    width = round(supersampled * coverage)
    height = round(mark.height * (width / mark.width))
    scaled = mark.resize((width, height), Image.LANCZOS)
    tile.alpha_composite(scaled, ((supersampled - width) // 2, (supersampled - height) // 2))

    return tile.resize((size, size), Image.LANCZOS)


def main() -> None:
    mark = load_mark()
    icons = BUILD / "icons"
    icons.mkdir(exist_ok=True)

    rendered = {size: render(mark, size, coverage) for size, coverage in COVERAGE.items()}
    for size, image in rendered.items():
        image.save(icons / f"{size}x{size}.png")

    frames = [
        rendered[size] if size in rendered else render(mark, size, COVERAGE[32])
        for size in ICO_SIZES
    ]
    frames[-1].save(
        BUILD / "icon.ico",
        format="ICO",
        sizes=[(size, size) for size in ICO_SIZES],
        append_images=frames[:-1],
    )

    print(f"wrote icons/ ({len(rendered)} sizes) and icon.ico ({len(ICO_SIZES)} sizes)")


if __name__ == "__main__":
    main()
