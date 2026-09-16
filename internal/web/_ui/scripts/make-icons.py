#!/usr/bin/env python3
"""Render the web client's favicons from the projmux app icon.

Source: internal/app/assets/projmux-icon.png (512x512, the same file as
docs/assets/projmux-icon.png; desktop notifications embed it, so it is only
read here). Output: public/assets/icon-{32,48,180}.png, which Vite copies
into the committed build. Run it from internal/web/_ui with Pillow installed
when the source icon changes, then `make web-build`:

    python3 scripts/make-icons.py
"""

from pathlib import Path

from PIL import Image

HERE = Path(__file__).resolve().parent.parent
SOURCE = HERE.parent.parent / "app" / "assets" / "projmux-icon.png"
OUT = HERE / "public" / "assets"

with Image.open(SOURCE) as icon:
    icon = icon.convert("RGBA")
    for size in (32, 48, 180):
        # No metadata is written, so the same source gives the same bytes.
        icon.resize((size, size), Image.LANCZOS).save(OUT / f"icon-{size}.png", optimize=True)
