#!/usr/bin/env python3
"""Write the Android app's launcher and notification icons from the Cloudfield
logo set's SVGs.

    python3 scripts/android-icons.py <aether-web checkout>/public/cloudfield

Converts cloudfield-android-foreground.svg and cloudfield-android-monochrome.svg
to VectorDrawables in android/app/src/main/res/drawable/. Path data is copied
verbatim, a nested <svg> becomes a <group> with the same translate and scale,
and a circle becomes the two-arc path through the same points. The
notification icon is the monochrome file's inner drawing at 24 dp,
moved by a translate only so that the bounding box of its stroked shapes is
centred in the box. The drawing's rings are open on the right, so that box is
not centred on the rings.
Anything else in an SVG stops the script, because VectorDrawable may not draw
it the same way. The adaptive background is a colour resource,
ic_launcher_background in res/values/colors.xml.
"""

import math
import re
import sys
import xml.etree.ElementTree as ET
from fractions import Fraction
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
DRAWABLE = ROOT / "android" / "app" / "src" / "main" / "res" / "drawable"
SVG = "{http://www.w3.org/2000/svg}"
PATH_ATTRS = {"d", "fill", "stroke", "stroke-width", "stroke-linejoin"}
CIRCLE_ATTRS = {"cx", "cy", "r", "fill"}
NESTED_ATTRS = {"x", "y", "width", "height", "viewBox"}


def number(value: Fraction) -> str:
    if value.denominator == 1:
        return str(value.numerator)
    return f"{float(value):.10f}".rstrip("0")


def viewbox(svg: ET.Element) -> Fraction:
    x, y, width, height = (Fraction(v) for v in svg.get("viewBox").split())
    if x or y or width != height:
        sys.exit(f"viewBox {svg.get('viewBox')}: only a square from 0 0 is supported")
    return width


def check(element: ET.Element, allowed: set) -> None:
    extra = set(element.attrib) - allowed
    if extra:
        sys.exit(f"<{element.tag.removeprefix(SVG)}> has unsupported attributes {sorted(extra)}")


def shape(element: ET.Element, indent: str) -> list:
    tag = element.tag.removeprefix(SVG)
    if tag == "path":
        check(element, PATH_ATTRS)
        fill, stroke = element.get("fill"), element.get("stroke")
        if fill is None and stroke is None:
            sys.exit("a path with SVG's default black fill is not supported")
        lines = [f"{indent}<path"]
        if fill not in (None, "none"):
            lines.append(f'{indent}    android:fillColor="{fill}"')
        if stroke:
            lines.append(f'{indent}    android:strokeColor="{stroke}"')
            lines.append(f'{indent}    android:strokeWidth="{element.get("stroke-width", "1")}"')
            if join := element.get("stroke-linejoin"):
                lines.append(f'{indent}    android:strokeLineJoin="{join}"')
        lines.append(f'{indent}    android:pathData="{element.get("d")}" />')
        return lines
    if tag == "circle":
        check(element, CIRCLE_ATTRS)
        cx, cy, r = (Fraction(element.get(k)) for k in ("cx", "cy", "r"))
        left, right, y, radius = number(cx - r), number(cx + r), number(cy), number(r)
        return [
            f"{indent}<path",
            f'{indent}    android:fillColor="{element.get("fill")}"',
            f'{indent}    android:pathData="M{left} {y}A{radius} {radius} 0 1 0 {right} {y}'
            f'A{radius} {radius} 0 1 0 {left} {y}Z" />',
        ]
    sys.exit(f"<{tag}> is not supported")


def nested(root: ET.Element) -> ET.Element:
    children = list(root)
    if len(children) != 1 or children[0].tag != SVG + "svg":
        sys.exit("expected exactly one nested <svg>")
    check(children[0], NESTED_ATTRS)
    if children[0].get("width") != children[0].get("height"):
        sys.exit("the nested <svg> must be square")
    return children[0]


def extent(element: ET.Element) -> tuple:
    """Return (xmin, xmax, ymin, ymax) of one shape, stroke width included.

    A stroked arc is an annular sector with butt caps, so its extremes are its
    four corners and the axis points of its outer edge that the arc crosses.
    """
    tag = element.tag.removeprefix(SVG)
    if tag == "circle":
        cx, cy, r = (float(element.get(k)) for k in ("cx", "cy", "r"))
        return cx - r, cx + r, cy - r, cy + r
    m = re.fullmatch(r"M([\d.]+) ([\d.]+)A(\d+) \3 0 ([01]) ([01]) ([\d.]+) ([\d.]+)", element.get("d"))
    if not m:
        sys.exit(f"cannot measure path {element.get('d')}: only M x y A r r 0 large sweep x y")
    x0, y0, r, large, sweep, x1, y1 = float(m[1]), float(m[2]), float(m[3]), m[4], m[5], float(m[6]), float(m[7])
    half = float(element.get("stroke-width", "1")) / 2
    mx, my, chord = (x0 + x1) / 2, (y0 + y1) / 2, math.hypot(x1 - x0, y1 - y0)
    offset = math.sqrt(r * r - chord * chord / 4)
    sign = -1 if large != sweep else 1
    cx = mx + sign * offset * (y1 - y0) / chord
    cy = my - sign * offset * (x1 - x0) / chord
    a0, a1 = math.atan2(y0 - cy, x0 - cx), math.atan2(y1 - cy, x1 - cx)
    start, end = (a0, a1) if sweep == "1" else (a1, a0)
    if end < start:
        end += math.tau
    angles = [start, end] + [k * math.pi / 2 for k in range(-8, 9) if start <= k * math.pi / 2 <= end]
    points = [(cx + (r + side * half) * math.cos(a), cy + (r + side * half) * math.sin(a))
              for a in angles[:2] for side in (-1, 1)]
    points += [(cx + (r + half) * math.cos(a), cy + (r + half) * math.sin(a)) for a in angles[2:]]
    xs, ys = [p[0] for p in points], [p[1] for p in points]
    return min(xs), max(xs), min(ys), max(ys)


def vector(source: Path, size_dp: int, viewport: Fraction, body: list) -> str:
    return "\n".join([
        '<?xml version="1.0" encoding="utf-8"?>',
        f"<!-- Generated from {source.name} by scripts/android-icons.py. -->",
        '<vector xmlns:android="http://schemas.android.com/apk/res/android"',
        f'    android:width="{size_dp}dp"',
        f'    android:height="{size_dp}dp"',
        f'    android:viewportWidth="{number(viewport)}"',
        f'    android:viewportHeight="{number(viewport)}">',
        *body,
        "</vector>",
        "",
    ])


def adaptive_layer(source: Path) -> str:
    root = ET.parse(source).getroot()
    inner = nested(root)
    scale = number(Fraction(inner.get("width")) / viewbox(inner))
    body = [
        "    <group",
        f'        android:translateX="{inner.get("x")}"',
        f'        android:translateY="{inner.get("y")}"',
        f'        android:scaleX="{scale}"',
        f'        android:scaleY="{scale}">',
    ]
    for element in inner:
        body += shape(element, "        ")
    body.append("    </group>")
    return vector(source, 108, viewbox(root), body)


def notification(source: Path) -> str:
    inner = nested(ET.parse(source).getroot())
    viewport = viewbox(inner)
    xmin, xmax, ymin, ymax = (f(v) for f, v in zip((min, max, min, max), zip(*map(extent, inner))))
    shift = [Fraction(round(float(viewport - low - high) / 2, 4)).limit_denominator(10000)
             for low, high in ((xmin, xmax), (ymin, ymax))]
    body = [
        "    <group",
        f'        android:translateX="{number(shift[0])}"',
        f'        android:translateY="{number(shift[1])}">',
    ]
    for element in inner:
        body += shape(element, "        ")
    body.append("    </group>")
    return vector(source, 24, viewport, body)


def main() -> None:
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    logo = Path(sys.argv[1])
    foreground = logo / "cloudfield-android-foreground.svg"
    monochrome = logo / "cloudfield-android-monochrome.svg"
    outputs = {
        "ic_launcher_foreground.xml": adaptive_layer(foreground),
        "ic_launcher_monochrome.xml": adaptive_layer(monochrome),
        "ic_stat_aether.xml": notification(monochrome),
    }
    for name, text in outputs.items():
        (DRAWABLE / name).write_text(text)
        print(f"wrote {(DRAWABLE / name).relative_to(ROOT)}")


if __name__ == "__main__":
    main()
