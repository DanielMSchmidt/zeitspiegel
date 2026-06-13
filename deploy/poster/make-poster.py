#!/usr/bin/env python3
"""Generate the printable Zeitspiegel "how to use" poster (A4 portrait SVG).

Dependency: segno (pure-Python QR generator).
    python3 -m venv .venv && .venv/bin/pip install segno
    .venv/bin/python deploy/poster/make-poster.py

The Wi-Fi is an open network (no password, E-7): the poster shows a second
QR that joins it on scan.

Options (env):
    URL    what the controls QR opens   (default http://zeitspiegel.local)
    SSID   Wi-Fi network name shown      (default zeitspiegel)

Writes deploy/poster/zeitspiegel-poster.svg — vector, scales to any paper.
"""
import os
import pathlib
import segno

URL = os.environ.get("URL", "http://zeitspiegel.local")
SSID = os.environ.get("SSID", "zeitspiegel")

# Palette — print-friendly: dark ink on white, one accent. QR stays pure black.
INK = "#14161a"
MUTE = "#5b6470"
ACCENT = "#2f6db0"
LINE = "#d7dbe0"

W, H = 800, 1131  # ~A4 at 96dpi (210×297mm ratio)


def qr_group(data, x, y, size, quiet=2):
    """Render a QR as black rects inside a size×size box at (x, y)."""
    qr = segno.make(data, error="m")
    matrix = list(qr.matrix)
    n = len(matrix) + 2 * quiet
    m = size / n
    out = [f'<rect x="{x:.1f}" y="{y:.1f}" width="{size:.1f}" '
           f'height="{size:.1f}" fill="#ffffff"/>']
    for r, row in enumerate(matrix):
        run = None
        for c, val in enumerate(list(row) + [0]):
            if val and run is None:
                run = c
            elif not val and run is not None:
                rx = x + (run + quiet) * m
                ry = y + (r + quiet) * m
                out.append(f'<rect x="{rx:.2f}" y="{ry:.2f}" '
                           f'width="{(c - run) * m:.2f}" height="{m:.2f}" fill="#000"/>')
                run = None
    return "\n".join(out)


def badge(n, cx, cy):
    return (f'<circle cx="{cx}" cy="{cy}" r="30" fill="{ACCENT}"/>'
            f'<text x="{cx}" y="{cy}" fill="#fff" font-size="34" font-weight="700" '
            f'text-anchor="middle" dominant-baseline="central">{n}</text>')


def wifi_icon(cx, cy, s=1.0):
    # three arcs + dot
    arcs = []
    for i, r in enumerate((34, 23, 12)):
        arcs.append(
            f'<path d="M {cx-r} {cy} A {r} {r} 0 0 1 {cx+r} {cy}" '
            f'fill="none" stroke="{ACCENT}" stroke-width="{6-i}" stroke-linecap="round"/>')
    arcs.append(f'<circle cx="{cx}" cy="{cy+2}" r="4.5" fill="{ACCENT}"/>')
    return "".join(arcs)


def phone_icon(cx, cy):
    return (f'<rect x="{cx-20}" y="{cy-34}" width="40" height="68" rx="7" '
            f'fill="none" stroke="{ACCENT}" stroke-width="5"/>'
            f'<circle cx="{cx}" cy="{cy+24}" r="3.2" fill="{ACCENT}"/>')


def sliders_icon(cx, cy):
    out = []
    for i, ty in enumerate((-16, 4, 24)):
        y = cy + ty
        knob = cx - 16 + (i * 16)
        out.append(f'<line x1="{cx-26}" y1="{y}" x2="{cx+26}" y2="{y}" '
                   f'stroke="{ACCENT}" stroke-width="5" stroke-linecap="round"/>')
        out.append(f'<circle cx="{knob}" cy="{y}" r="7" fill="#fff" '
                   f'stroke="{ACCENT}" stroke-width="5"/>')
    return "".join(out)


def download_icon(cx, cy):
    return (f'<line x1="{cx}" y1="{cy-26}" x2="{cx}" y2="{cy+10}" '
            f'stroke="{ACCENT}" stroke-width="5" stroke-linecap="round"/>'
            f'<path d="M {cx-14} {cy-4} L {cx} {cy+12} L {cx+14} {cy-4}" '
            f'fill="none" stroke="{ACCENT}" stroke-width="5" '
            f'stroke-linecap="round" stroke-linejoin="round"/>'
            f'<line x1="{cx-22}" y1="{cy+26}" x2="{cx+22}" y2="{cy+26}" '
            f'stroke="{ACCENT}" stroke-width="5" stroke-linecap="round"/>')


def text(x, y, s, size=22, weight=400, fill=INK, anchor="start", spacing=None):
    sp = f' letter-spacing="{spacing}"' if spacing else ""
    return (f'<text x="{x}" y="{y}" fill="{fill}" font-size="{size}" '
            f'font-weight="{weight}" text-anchor="{anchor}"{sp}>{s}</text>')


parts = [
    f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" '
    f'font-family="Helvetica, Arial, sans-serif">',
    f'<rect width="{W}" height="{H}" fill="#ffffff"/>',
    f'<rect x="20" y="20" width="{W-40}" height="{H-40}" rx="22" '
    f'fill="none" stroke="{LINE}" stroke-width="2"/>',
    # header
    text(W / 2, 112, "Zeitspiegel", size=58, weight=700, anchor="middle"),
    text(W / 2, 150, "THE DELAYED DANCE MIRROR", size=20, weight=600,
         fill=ACCENT, anchor="middle", spacing="3"),
    text(W / 2, 200, "Dance now — watch yourself a few seconds later.",
         size=24, fill=MUTE, anchor="middle"),
    f'<line x1="64" y1="236" x2="{W-64}" y2="236" stroke="{LINE}" stroke-width="2"/>',
]

# Step 1 — Wi-Fi (open network)
y = 300
parts += [badge("1", 100, y + 20), wifi_icon(200, y + 22)]
parts += [text(250, y, "Connect to the Wi-Fi", size=30, weight=700)]
parts += [text(250, y + 38, "Network", size=20, fill=MUTE),
          text(360, y + 38, SSID, size=22, weight=700)]
parts += [text(250, y + 70, "No password — just connect.", size=20, fill=MUTE)]
# Open-network join QR: phones connect on scan (no secret in it). Escape per spec.
esc = SSID.translate(str.maketrans({c: "\\" + c for c in r'\;,:"'}))
parts += [qr_group(f"WIFI:S:{esc};T:nopass;;", 600, y - 34, 120)]
parts += [text(660, y + 104, "scan to join", size=16, fill=MUTE, anchor="middle")]

# Step 2 — scan QR
y = 446
parts += [badge("2", 100, y + 20), phone_icon(200, y + 22)]
parts += [text(250, y, "Scan to open the controls", size=30, weight=700)]
parts += [text(250, y + 38, "or type it into any browser:", size=20, fill=MUTE)]
parts += [text(250, y + 70, URL.replace("http://", ""), size=24, weight=700, fill=ACCENT)]
QR = 250
parts += [qr_group(URL, (W - QR) / 2, y + 100, QR)]
parts += [text(W / 2, y + 100 + QR + 28, "point your camera here",
               size=18, fill=MUTE, anchor="middle")]

# Step 3 — use it
y = 880
parts += [badge("3", 100, y + 20)]
parts += [sliders_icon(190, y + 12), download_icon(250, y + 16)]
parts += [text(300, y, "Set the delay · save clips", size=30, weight=700)]
parts += [text(300, y + 38, "Drag the slider to choose how many seconds", size=20, fill=MUTE)]
parts += [text(300, y + 64, "late the mirror shows you.", size=20, fill=MUTE)]
parts += [text(300, y + 96, "Download the last clip to keep it.", size=20, fill=MUTE)]

# footer
parts += [
    f'<line x1="64" y1="{H-92}" x2="{W-64}" y2="{H-92}" stroke="{LINE}" stroke-width="2"/>',
    text(W / 2, H - 56, "No internet needed — everything stays in this room.",
         size=20, weight=600, anchor="middle"),
    text(W / 2, H - 30, "Nothing is recorded unless you download a clip.",
         size=18, fill=MUTE, anchor="middle"),
    "</svg>",
]

out = pathlib.Path(__file__).with_name("zeitspiegel-poster.svg")
out.write_text("\n".join(parts))
print("wrote", out)
