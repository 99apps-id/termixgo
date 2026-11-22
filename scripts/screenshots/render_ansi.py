#!/usr/bin/env python3
"""Render tmux capture-pane ANSI frames as real terminal PNGs.

Replaces the ansi2html + browser step of scripts/screenshots: the frame is
replayed through a terminal emulator grid (pyte) and drawn cell by cell with
JetBrains Mono, so block glyphs connect, borders stay solid and colours are
the terminal's own. No window chrome is added.
"""
import os
import sys

import pyte
from PIL import Image, ImageDraw, ImageFont

BASE = os.path.dirname(os.path.abspath(__file__))
FONT = os.path.join(BASE, "fonts", "JetBrainsMono-Regular.ttf")
FONTB = os.path.join(BASE, "fonts", "JetBrainsMono-Bold.ttf")
FSIZE = 22
COLS = 118
PAD = 12

NAMED = {
    "black": (12, 12, 20), "red": (255, 85, 85), "green": (80, 250, 123),
    "yellow": (241, 250, 140), "blue": (98, 114, 164), "magenta": (255, 121, 198),
    "cyan": (139, 233, 253), "white": (248, 248, 242),
    "brightblack": (98, 114, 164), "brightred": (255, 110, 110),
    "brightgreen": (105, 255, 160), "brightyellow": (255, 246, 160),
    "brightblue": (130, 170, 255), "brightmagenta": (255, 160, 220),
    "brightcyan": (160, 245, 255), "brightwhite": (255, 255, 255),
}
DEFAULT_FG = (226, 228, 234)
DEFAULT_BG = (13, 14, 22)


def hexcol(h, default):
    try:
        h = h.lstrip("#")
        return tuple(int(h[i:i + 2], 16) for i in (0, 2, 4))
    except Exception:
        return default


def render(ansi_path, out_path):
    raw = open(ansi_path, encoding="utf-8").read()
    lines = raw.split("\n")
    while lines and lines[-1] == "":
        lines.pop()
    screen = pyte.Screen(COLS, len(lines))
    stream = pyte.Stream(screen)
    # Replay line by line with absolute cursor placement: a capture-pane dump
    # is a finished screen, not a stream, so autowrap must never add rows.
    for i, line in enumerate(lines):
        stream.feed(f"\x1b[{i + 1};1H" + line)
    rows = len(lines)
    # last row that carries any glyph, plus one blank row of breathing room
    last = 0
    for y in range(rows):
        if any((screen.buffer[y][x].data or " ") != " " for x in range(COLS)):
            last = y
    rows_out = last + 2
    f = ImageFont.truetype(FONT, FSIZE)
    fb = ImageFont.truetype(FONTB, FSIZE)
    cw = round(f.getlength("M"))
    ch = int(FSIZE * 1.32)
    img = Image.new("RGB", (COLS * cw + PAD * 2, rows_out * ch + PAD * 2), DEFAULT_BG)
    d = ImageDraw.Draw(img)
    for y in range(rows_out):
        for x in range(COLS):
            c = screen.buffer[y][x]
            fg = NAMED.get(c.fg, None) or (hexcol(c.fg, DEFAULT_FG) if len(str(c.fg)) >= 6 else DEFAULT_FG)
            bg = NAMED.get(c.bg, None) or (hexcol(c.bg, DEFAULT_BG) if len(str(c.bg)) >= 6 else DEFAULT_BG)
            if c.reverse:
                fg, bg = bg, fg
            if bg != DEFAULT_BG:
                d.rectangle([PAD + x * cw, PAD + y * ch, PAD + (x + 1) * cw, PAD + (y + 1) * ch], fill=bg)
            data = c.data or " "
            if data != " ":
                d.text((PAD + x * cw, PAD + y * ch), data, font=fb if c.bold else f, fill=fg)
    img.save(out_path)
    print(out_path, img.size)


if __name__ == "__main__":
    render(sys.argv[1], sys.argv[2])
