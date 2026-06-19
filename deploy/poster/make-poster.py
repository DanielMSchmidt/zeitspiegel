#!/usr/bin/env python3
"""Generate the printable Zeitspiegel "how to use" poster (A4 portrait SVG).

Dependency: segno (pure-Python QR generator).
    python3 -m venv .venv && .venv/bin/pip install segno
    .venv/bin/python deploy/poster/make-poster.py

The Wi-Fi is an open network (no password, E-7): the poster shows a Wi-Fi
join QR that connects the phone on scan, then a second QR that opens the
controls page once connected.

Options (env):
    URL    what the controls QR opens   (default http://zeitspiegel.local)
    IP     always-works typed address   (default 10.42.0.1)
    SSID   Wi-Fi network name shown      (default zeitspiegel)

Writes deploy/poster/zeitspiegel-poster.svg — vector, scales to any paper.
"""
import os
import pathlib
import segno

URL = os.environ.get("URL", "http://zeitspiegel.local")
IP = os.environ.get("IP", "10.42.0.1")
SSID = os.environ.get("SSID", "zeitspiegel")

# Palette — print-friendly: dark ink on white, one accent. QR stays pure black.
INK = "#14161a"
MUTE = "#5b6470"
ACCENT = "#2f6db0"
WARN_BG = "#fff4d6"
WARN_BORDER = "#e0a900"
WARN_INK = "#5a3d00"
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


def badge(n, cx, cy, r=26):
    return (f'<circle cx="{cx}" cy="{cy}" r="{r}" fill="{ACCENT}"/>'
            f'<text x="{cx}" y="{cy}" fill="#fff" font-size="{int(r*1.15)}" '
            f'font-weight="700" text-anchor="middle" '
            f'dominant-baseline="central">{n}</text>')


def arrow(x1, y, x2):
    head = 14
    return (f'<line x1="{x1}" y1="{y}" x2="{x2 - head}" y2="{y}" '
            f'stroke="{ACCENT}" stroke-width="6" stroke-linecap="round"/>'
            f'<path d="M {x2 - head} {y - 10} L {x2} {y} L {x2 - head} {y + 10} Z" '
            f'fill="{ACCENT}"/>')


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
    # header — compact, leaves room for QRs near the top
    text(W / 2, 80, "Zeitspiegel", size=48, weight=700, anchor="middle"),
    text(W / 2, 110, "THE DELAYED DANCE MIRROR", size=16, weight=600,
         fill=ACCENT, anchor="middle", spacing="3"),
    text(W / 2, 156, "Scan these two — in order:",
         size=24, weight=600, fill=INK, anchor="middle"),
]

# Two equal-sized QR codes, side by side, with badges above.
QR = 260
gap = 60
total = QR * 2 + gap
left_x = (W - total) / 2
right_x = left_x + QR + gap
left_cx = left_x + QR / 2
right_cx = right_x + QR / 2

# Row 1: numbered badges (with arrow between them).
badge_cy = 210
parts += [badge("1", left_cx, badge_cy, r=26)]
parts += [badge("2", right_cx, badge_cy, r=26)]
parts += [arrow(left_cx + 50, badge_cy, right_cx - 50)]

# Row 2: caption directly under each badge, just above its QR.
cap_top_y = 270
parts += [text(left_cx, cap_top_y, "Join the Wi-Fi",
               size=22, weight=700, anchor="middle")]
parts += [text(right_cx, cap_top_y, "Open the controls",
               size=22, weight=700, anchor="middle")]

qr_y = 290

# The QR codes themselves — identical size.
esc = SSID.translate(str.maketrans({c: "\\" + c for c in r'\;,:"'}))
parts += [qr_group(f"WIFI:S:{esc};T:nopass;;", left_x, qr_y, QR)]
parts += [qr_group(URL, right_x, qr_y, QR)]

# Captions under QRs — network name / URL
cap_y = qr_y + QR + 28
parts += [text(left_cx, cap_y, "Network", size=15, fill=MUTE, anchor="middle")]
parts += [text(left_cx, cap_y + 24, SSID, size=22, weight=700, anchor="middle")]
parts += [text(right_cx, cap_y, "or type in any browser",
               size=15, fill=MUTE, anchor="middle")]
parts += [text(right_cx, cap_y + 24, IP, size=22, weight=700,
               fill=ACCENT, anchor="middle")]

# "Stay Connected" warning — the critical bit between joining Wi-Fi and step 2.
warn_y = cap_y + 60
warn_h = 96
parts += [
    f'<rect x="64" y="{warn_y}" width="{W-128}" height="{warn_h}" rx="14" '
    f'fill="{WARN_BG}" stroke="{WARN_BORDER}" stroke-width="2"/>',
    # exclamation triangle
    f'<path d="M 110 {warn_y + 34} L 138 {warn_y + 82} L 82 {warn_y + 82} Z" '
    f'fill="none" stroke="{WARN_BORDER}" stroke-width="4" stroke-linejoin="round"/>',
    f'<line x1="110" y1="{warn_y + 50}" x2="110" y2="{warn_y + 68}" '
    f'stroke="{WARN_BORDER}" stroke-width="4" stroke-linecap="round"/>',
    f'<circle cx="110" cy="{warn_y + 76}" r="2.8" fill="{WARN_BORDER}"/>',
    text(160, warn_y + 40,
         "Your phone will say “No internet connection”.",
         size=19, weight=700, fill=WARN_INK),
    f'<text x="160" y="{warn_y + 68}" fill="{WARN_INK}" font-size="18" '
    f'font-weight="400">'
    f'That’s expected — tap '
    f'<tspan font-weight="700">“Stay connected”</tspan>'
    f' (or “Keep trying” / “Yes”).'
    f'</text>',
]

# divider before the "what & how" explanation
exp_y = warn_y + warn_h + 36
parts += [f'<line x1="64" y1="{exp_y}" x2="{W-64}" y2="{exp_y}" '
          f'stroke="{LINE}" stroke-width="2"/>']

# Then the rest — what it is and what you can do.
y = exp_y + 50
parts += [text(W / 2, y, "Dance now — watch yourself a few seconds later.",
               size=24, weight=700, anchor="middle")]
y += 36
parts += [text(W / 2, y,
               "The camera shows you on a delay. Move, then look up.",
               size=18, fill=MUTE, anchor="middle")]

# Bullet rows: delay slider + download
y += 56
parts += [
    f'<circle cx="120" cy="{y - 6}" r="6" fill="{ACCENT}"/>',
    text(146, y, "Drag the slider", size=20, weight=700),
    text(146, y + 26, "to choose how many seconds late the mirror shows you.",
         size=18, fill=MUTE),
]
y += 64
parts += [
    f'<circle cx="120" cy="{y - 6}" r="6" fill="{ACCENT}"/>',
    text(146, y, "Tap download", size=20, weight=700),
    text(146, y + 26, "to save the last clip to your phone.",
         size=18, fill=MUTE),
]

# footer
parts += [
    f'<line x1="64" y1="{H-92}" x2="{W-64}" y2="{H-92}" stroke="{LINE}" stroke-width="2"/>',
    text(W / 2, H - 56, "No internet needed — everything stays in this room.",
         size=19, weight=600, anchor="middle"),
    text(W / 2, H - 30, "Nothing is recorded unless you download a clip.",
         size=17, fill=MUTE, anchor="middle"),
    "</svg>",
]

out = pathlib.Path(__file__).with_name("zeitspiegel-poster.svg")
out.write_text("\n".join(parts))
print("wrote", out)
