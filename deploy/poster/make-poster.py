#!/usr/bin/env python3
"""Generate the printable Zeitspiegel guest poster (A4 portrait SVG).

Dependency: segno (pure-Python QR generator).
    python3 -m venv .venv && .venv/bin/pip install segno
    .venv/bin/python deploy/poster/make-poster.py

The poster reads top to bottom in the order a guest needs it: what the thing
in front of them is for, then — only then — the two QR codes that get their
phone to the delay slider. The Wi-Fi is an open network (no password, E-7),
so code 1 joins it on scan and code 2 opens the controls once connected.

Three variants are written on every run: a bilingual one (German lead,
English underneath) plus a German-only and an English-only poster for rooms
that want just one language.

Layout is a flow, not fixed coordinates: text is measured, wrapped and
stacked, so a longer German line or a longer network name pushes what
follows down instead of colliding with it. `LayoutError` is raised rather
than silently emitting a poster that runs off the page.

Options (env):
    URL    what the controls QR opens   (default http://zeitspiegel.local)
    IP     always-works typed address   (default 10.42.0.1)
    SSID   Wi-Fi network name shown     (default zeitspiegel)
    DELAY  boot delay shown, seconds    (default 25, = config default_delay_s)

Writes deploy/poster/zeitspiegel-poster[-de|-en].svg (the print masters) and
site/poster[-de|-en].svg (embedded by site/guests.html). Each pair is
byte-identical — this script is the single source of truth, so they cannot
drift; the CI `poster` job re-runs it and fails on any diff.
"""
import html
import os
import pathlib

import segno

URL = os.environ.get("URL", "http://zeitspiegel.local")
IP = os.environ.get("IP", "10.42.0.1")
SSID = os.environ.get("SSID", "zeitspiegel")
DELAY = os.environ.get("DELAY", "25")

# German leads, English follows. Every key must exist in both languages and
# be rendered on the poster — UT-30 asserts both.
STRINGS = {
    "de": {
        "subtitle": "DER TANZSPIEGEL MIT VERZÖGERUNG",
        "lead": "Tanzen. Kurz warten. Sich selbst sehen.",
        "intro": "Der Bildschirm zeigt dich mit ein paar Sekunden Verzögerung.",
        "step_dance": "Tanzen, ohne hinzuschauen",
        "step_wait": "{delay} Sekunden warten",
        "step_watch": "Dir selbst zusehen",
        "qr_heading": "Verzögerung selbst einstellen",
        "cap_wifi": "WLAN verbinden",
        "cap_controls": "Steuerung öffnen",
        "label_network": "Netzwerk",
        "label_browser": "oder im Browser eingeben",
        "controls_note": "Schieberegler = Sekunden. Download = der letzte Clip "
                         "aufs Handy.",
        "warn": "„Kein Internet“ ist normal — tippe auf „Verbunden bleiben“.",
        "footer": "Kein Internet, keine Aufnahme — außer du lädst selbst einen "
                  "Clip herunter.",
    },
    "en": {
        "subtitle": "THE DANCE MIRROR WITH A DELAY",
        "lead": "Dance. Wait a moment. Watch yourself.",
        "intro": "The screen shows you a few seconds late.",
        "step_dance": "Dance without watching",
        "step_wait": "wait {delay} seconds",
        "step_watch": "Watch it back",
        "qr_heading": "Set the delay yourself",
        "cap_wifi": "Join the Wi-Fi",
        "cap_controls": "Open the controls",
        "label_network": "Network",
        "label_browser": "or type in any browser",
        "controls_note": "Slider = seconds. Download = the last clip on your phone.",
        "warn": "“No internet” is normal — tap “Stay connected”.",
        "footer": "No internet, no recording — unless you download a clip yourself.",
    },
}

def strings(lang, delay=DELAY):
    """The strings for one language, with the boot delay filled in."""
    return {k: v.format(delay=delay) for k, v in STRINGS[lang].items()}


# Palette — print-friendly: dark ink on white, one accent. QR stays pure black.
INK = "#14161a"
MUTE = "#5b6470"
ACCENT = "#2f6db0"
WARN_BG = "#fff4d6"
WARN_BORDER = "#e0a900"
WARN_INK = "#5a3d00"
LINE = "#d7dbe0"

W, H = 800, 1131  # ~A4 at 96dpi (210×297mm ratio)
MARGIN = 64
COL = W - 2 * MARGIN
FOOTER_TOP = H - 76   # the footer rule
FOOTER_CLEAR = 18     # air the content must leave above that rule


class LayoutError(Exception):
    """The composed poster does not fit the page."""


# --- text metrics -----------------------------------------------------------
# Helvetica/Arial advance widths (units per 1000 em) for the characters the
# poster actually uses. Wrapping without metrics is how a German line ends up
# half an inch into the margin, so the widths are carried here rather than
# guessed from character counts.
_W = {
    " ": 278, "!": 278, '"': 355, "&": 667, "'": 191, "(": 333, ")": 333,
    ",": 278, "-": 333, ".": 278, "/": 278, ":": 278, ";": 278, "?": 556,
    "A": 667, "B": 667, "C": 722, "D": 722, "E": 667, "F": 611, "G": 778,
    "H": 722, "I": 278, "J": 500, "K": 667, "L": 556, "M": 833, "N": 722,
    "O": 778, "P": 667, "Q": 778, "R": 722, "S": 667, "T": 611, "U": 722,
    "V": 667, "W": 944, "X": 667, "Y": 667, "Z": 611,
    "a": 556, "b": 556, "c": 500, "d": 556, "e": 556, "f": 278, "g": 556,
    "h": 556, "i": 222, "j": 222, "k": 500, "l": 222, "m": 833, "n": 556,
    "o": 556, "p": 556, "q": 556, "r": 333, "s": 500, "t": 278, "u": 556,
    "v": 500, "w": 722, "x": 500, "y": 500, "z": 500,
    "Ä": 667, "Ö": 778, "Ü": 722, "ä": 556, "ö": 556, "ü": 556, "ß": 611,
    "–": 556, "—": 1000, "·": 278, "„": 333, "“": 333, "”": 333, "‘": 222,
    "’": 222, "…": 1000, "°": 400,
}
_DEFAULT_W = 556


def text_width(s, size, weight=400, spacing=0):
    """Approximate rendered width of `s` in user units."""
    em = sum(_W.get(c, _DEFAULT_W) for c in s) / 1000.0
    bold = 1.06 if weight >= 600 else (1.03 if weight >= 500 else 1.0)
    return em * size * bold * 1.02 + spacing * len(s)  # 2 % safety margin


def wrap(s, size, weight, max_w):
    """Greedy word wrap at `max_w`. Returns at least one line."""
    lines, cur = [], ""
    for word in s.split():
        trial = f"{cur} {word}".strip()
        if cur and text_width(trial, size, weight) > max_w:
            lines.append(cur)
            cur = word
        else:
            cur = trial
    lines.append(cur)
    return lines


def extent(t):
    """Left and right edge of a recorded text run."""
    w = t["width"]
    if t["anchor"] == "middle":
        return t["x"] - w / 2, t["x"] + w / 2
    if t["anchor"] == "end":
        return t["x"] - w, t["x"]
    return t["x"], t["x"] + w


# --- document ---------------------------------------------------------------
class Doc:
    """Collects SVG fragments plus a record of every text run and QR box, so
    the layout can be asserted (UT-30) instead of eyeballed on a printout."""

    def __init__(self):
        self.parts = []
        self.texts = []
        self.qrs = []
        self.bottom = 0.0
        self.content_bottom = 0.0

    def raw(self, fragment):
        self.parts.append(fragment)

    def text(self, x, y, s, size=18, weight=400, fill=INK, anchor="start",
             spacing=0, lang=None, src=None):
        sp = f' letter-spacing="{spacing}"' if spacing else ""
        self.parts.append(
            f'<text x="{x:.1f}" y="{y:.1f}" fill="{fill}" font-size="{size}" '
            f'font-weight="{weight}" text-anchor="{anchor}"{sp}>'
            f'{html.escape(s)}</text>')
        self.texts.append({
            "x": x, "y": y, "s": s, "src": src if src is not None else s,
            "size": size, "weight": weight, "anchor": anchor, "lang": lang,
            "width": text_width(s, size, weight, spacing),
        })
        self.bottom = max(self.bottom, y)
        return y

    def para(self, x, y, s, size=18, weight=400, fill=INK, anchor="middle",
             max_w=COL, leading=None, lang=None):
        """Wrap `s` to `max_w` and stack the lines. Returns the next free y."""
        leading = leading or round(size * 1.32)
        for line in wrap(s, size, weight, max_w):
            self.text(x, y, line, size=size, weight=weight, fill=fill,
                      anchor=anchor, lang=lang, src=s)
            y += leading
        return y - leading

    def qr(self, payload, x, y, size, quiet=2):
        """Render a QR as black rects inside a size×size box at (x, y)."""
        matrix = [list(row) for row in segno.make(payload, error="m").matrix]
        n = len(matrix) + 2 * quiet
        m = size / n
        self.parts.append(f'<rect x="{x:.1f}" y="{y:.1f}" width="{size:.1f}" '
                          f'height="{size:.1f}" fill="#ffffff"/>')
        for r, row in enumerate(matrix):
            run = None
            for c, val in enumerate(row + [0]):
                if val and run is None:
                    run = c
                elif not val and run is not None:
                    self.parts.append(
                        f'<rect x="{x + (run + quiet) * m:.2f}" '
                        f'y="{y + (r + quiet) * m:.2f}" '
                        f'width="{(c - run) * m:.2f}" height="{m:.2f}" '
                        f'fill="#000"/>')
                    run = None
        self.qrs.append({"payload": payload, "x": x, "y": y, "size": size,
                         "quiet": quiet})
        self.bottom = max(self.bottom, y + size)

    def svg(self):
        return "\n".join(
            [f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" '
             f'font-family="Helvetica, Arial, sans-serif">'] + self.parts
            + ["</svg>"])


# --- drawing helpers --------------------------------------------------------
def badge(d, n, cx, cy, r=24):
    d.raw(f'<circle cx="{cx:.1f}" cy="{cy:.1f}" r="{r}" fill="{ACCENT}"/>')
    d.raw(f'<text x="{cx:.1f}" y="{cy:.1f}" fill="#fff" font-size="{int(r*1.15)}" '
          f'font-weight="700" text-anchor="middle" '
          f'dominant-baseline="central">{n}</text>')


def arrow(d, x1, y, x2, color=ACCENT, width=5):
    head = 12
    d.raw(f'<line x1="{x1:.1f}" y1="{y:.1f}" x2="{x2 - head:.1f}" y2="{y:.1f}" '
          f'stroke="{color}" stroke-width="{width}" stroke-linecap="round"/>')
    d.raw(f'<path d="M {x2 - head:.1f} {y - 9:.1f} L {x2:.1f} {y:.1f} '
          f'L {x2 - head:.1f} {y + 9:.1f} Z" fill="{color}"/>')


def rule(d, y):
    d.raw(f'<line x1="{MARGIN}" y1="{y:.1f}" x2="{W - MARGIN}" y2="{y:.1f}" '
          f'stroke="{LINE}" stroke-width="2"/>')


def icon_dancer(d, cx, cy):
    s = f'stroke="{ACCENT}" stroke-width="4" stroke-linecap="round" fill="none"'
    d.raw(f'<circle cx="{cx - 2:.1f}" cy="{cy - 22:.1f}" r="7" fill="{ACCENT}"/>')
    d.raw(f'<path d="M {cx - 2:.1f} {cy - 14:.1f} L {cx + 2:.1f} {cy + 2:.1f}" {s}/>')
    d.raw(f'<path d="M {cx - 16:.1f} {cy - 2:.1f} L {cx:.1f} {cy - 10:.1f} '
          f'L {cx + 15:.1f} {cy - 14:.1f}" {s}/>')
    d.raw(f'<path d="M {cx + 2:.1f} {cy + 2:.1f} L {cx - 10:.1f} {cy + 18:.1f}" {s}/>')
    d.raw(f'<path d="M {cx + 2:.1f} {cy + 2:.1f} L {cx + 14:.1f} {cy + 14:.1f}" {s}/>')


def icon_clock(d, cx, cy):
    s = f'stroke="{ACCENT}" stroke-width="4" stroke-linecap="round" fill="none"'
    d.raw(f'<circle cx="{cx:.1f}" cy="{cy - 4:.1f}" r="19" {s}/>')
    d.raw(f'<path d="M {cx:.1f} {cy - 16:.1f} L {cx:.1f} {cy - 4:.1f} '
          f'L {cx + 10:.1f} {cy + 1:.1f}" {s}/>')


def icon_screen(d, cx, cy):
    """Beat three: the same dancer, on the screen."""
    d.raw(f'<rect x="{cx - 27:.1f}" y="{cy - 24:.1f}" width="54" height="44" '
          f'rx="6" fill="none" stroke="{ACCENT}" stroke-width="4" '
          f'stroke-linejoin="round"/>')
    s = f'stroke="{ACCENT}" stroke-width="3" stroke-linecap="round" fill="none"'
    d.raw(f'<circle cx="{cx - 1:.1f}" cy="{cy - 13:.1f}" r="4.5" fill="{ACCENT}"/>')
    d.raw(f'<path d="M {cx - 1:.1f} {cy - 8:.1f} L {cx + 1:.1f} {cy + 2:.1f}" {s}/>')
    d.raw(f'<path d="M {cx - 10:.1f} {cy + 1:.1f} L {cx:.1f} {cy - 5:.1f} '
          f'L {cx + 9:.1f} {cy - 8:.1f}" {s}/>')
    d.raw(f'<path d="M {cx + 1:.1f} {cy + 2:.1f} L {cx - 6:.1f} {cy + 12:.1f} '
          f'M {cx + 1:.1f} {cy + 2:.1f} L {cx + 8:.1f} {cy + 10:.1f}" {s}/>')


# --- the poster -------------------------------------------------------------
def build(langs=("de", "en"), url=URL, ip=IP, ssid=SSID, delay=DELAY):
    """Compose the poster in `langs` order (first language leads)."""
    langs = tuple(langs)
    S = [strings(lang, delay) for lang in langs]
    # A second language doubles every line of copy, so the air between the
    # sections — not the type — is what gives. One knob, tuned per variant.
    air = 1.0 if len(langs) > 1 else 1.95
    qr_size = 168 if len(langs) > 1 else 248

    def s(i, key):
        return S[i][key]

    def lead(i):
        """Typography for language i: the first one leads, the rest support."""
        return (INK, 700) if i == 0 else (MUTE, 400)

    d = Doc()
    d.raw(f'<rect width="{W}" height="{H}" fill="#ffffff"/>')
    d.raw(f'<rect x="20" y="20" width="{W - 40}" height="{H - 40}" rx="22" '
          f'fill="none" stroke="{LINE}" stroke-width="2"/>')

    # --- masthead
    y = 72
    d.text(W / 2, y, "Zeitspiegel", size=46, weight=700, anchor="middle")
    y += 30
    for i, lang in enumerate(langs):
        size = 15 if i == 0 else 13
        d.text(W / 2, y, s(i, "subtitle"), size=size, weight=700 if i == 0 else 500,
               fill=ACCENT if i == 0 else MUTE, anchor="middle", spacing=2.5,
               lang=lang)
        y += size + 8

    # --- what the thing is for, before anything asks the guest to do something
    y += 18 * air
    for i, lang in enumerate(langs):
        fill, _ = lead(i)
        size = 30 if i == 0 else 22
        y = d.para(W / 2, y, s(i, "lead"), size=size, weight=700, fill=fill,
                   lang=lang) + size + (10 if i == 0 else 4)

    y += 4 * air
    for i, lang in enumerate(langs):
        size = 18 if i == 0 else 16
        y = d.para(W / 2, y, s(i, "intro"), size=size,
                   fill=INK if i == 0 else MUTE, max_w=COL - 60,
                   lang=lang) + size + (10 if i == 0 else 4)

    # --- the same idea in three beats: dance -> wait -> see yourself
    y += 26 * air
    cxs = (MARGIN + 120, W / 2, W - MARGIN - 120)
    for cx, icon in zip(cxs, (icon_dancer, icon_clock, icon_screen)):
        icon(d, cx, y)
    for a, b in zip(cxs, cxs[1:]):
        arrow(d, a + 54, y - 4, b - 54)

    y += 42
    for i, lang in enumerate(langs):
        fill, weight = lead(i)
        size = 16 if i == 0 else 14.5
        for cx, key in zip(cxs, ("step_dance", "step_wait", "step_watch")):
            d.text(cx, y, s(i, key), size=size, weight=weight, fill=fill,
                   anchor="middle", lang=lang)
        y += size + 6

    # --- and how to change it: the two codes
    y += 16 * air
    rule(d, y)
    y += 22 + 10 * air
    for i, lang in enumerate(langs):
        fill, _ = lead(i)
        size = 25 if i == 0 else 19
        y = d.para(W / 2, y, s(i, "qr_heading"), size=size, weight=700,
                   fill=fill, lang=lang) + size + 5
    # Two equal codes side by side, each under its numbered badge: join the
    # network, then open the page with the slider on it.
    y += 14 + 12 * air
    gap = 64
    left_x = (W - (qr_size * 2 + gap)) / 2
    right_x = left_x + qr_size + gap
    left_cx, right_cx = left_x + qr_size / 2, right_x + qr_size / 2

    badge(d, "1", left_cx, y)
    badge(d, "2", right_cx, y)
    arrow(d, left_cx + 46, y, right_cx - 46)
    y += 44

    for i, lang in enumerate(langs):
        fill, weight = (INK, 700) if i == 0 else (MUTE, 500)
        size = 19 if i == 0 else 16
        d.text(left_cx, y, s(i, "cap_wifi"), size=size, weight=weight,
               fill=fill, anchor="middle", lang=lang)
        d.text(right_cx, y, s(i, "cap_controls"), size=size, weight=weight,
               fill=fill, anchor="middle", lang=lang)
        y += size + 5

    y += 6
    esc = ssid.translate(str.maketrans({c: "\\" + c for c in r'\;,:"'}))
    d.qr(f"WIFI:S:{esc};T:nopass;;", left_x, y, qr_size)
    d.qr(url, right_x, y, qr_size)
    y += qr_size + 22

    # What each code lands you on: the network name, and the typed fallback
    # for the phone whose camera refuses the second code.
    for i, lang in enumerate(langs):
        size = 14 if i == 0 else 12.5
        d.text(left_cx, y, s(i, "label_network"), size=size, fill=MUTE,
               anchor="middle", lang=lang)
        d.text(right_cx, y, s(i, "label_browser"), size=size, fill=MUTE,
               anchor="middle", lang=lang)
        y += size + 4
    y += 16
    d.text(left_cx, y, ssid, size=21, weight=700, anchor="middle")
    d.text(right_cx, y, ip, size=21, weight=700, fill=ACCENT, anchor="middle")

    y += 24 + 12 * air
    for i, lang in enumerate(langs):
        size = 16 if i == 0 else 14.5
        y = d.para(W / 2, y, s(i, "controls_note"), size=size,
                   fill=INK if i == 0 else MUTE, max_w=COL - 40,
                   lang=lang) + size + 4

    # --- the one thing phones get wrong: "no internet" is not a failure
    y += 20 + 12 * air
    warn_sizes = [17 if i == 0 else 15 for i in range(len(langs))]
    box_h = sum(size + 9 for size in warn_sizes) + 24
    d.raw(f'<rect x="{MARGIN}" y="{y:.1f}" width="{COL}" height="{box_h:.1f}" '
          f'rx="14" fill="{WARN_BG}" stroke="{WARN_BORDER}" stroke-width="2"/>')
    icon_cy = y + box_h / 2
    d.raw(f'<path d="M {MARGIN + 46:.1f} {icon_cy - 19:.1f} '
          f'L {MARGIN + 69:.1f} {icon_cy + 19:.1f} '
          f'L {MARGIN + 23:.1f} {icon_cy + 19:.1f} Z" fill="none" '
          f'stroke="{WARN_BORDER}" stroke-width="4" stroke-linejoin="round"/>')
    d.raw(f'<line x1="{MARGIN + 46:.1f}" y1="{icon_cy - 5:.1f}" '
          f'x2="{MARGIN + 46:.1f}" y2="{icon_cy + 7:.1f}" stroke="{WARN_BORDER}" '
          f'stroke-width="4" stroke-linecap="round"/>')
    d.raw(f'<circle cx="{MARGIN + 46:.1f}" cy="{icon_cy + 14:.1f}" r="2.6" '
          f'fill="{WARN_BORDER}"/>')

    ty = y + 24
    for i, lang in enumerate(langs):
        size = warn_sizes[i]
        d.text(MARGIN + 96, ty, s(i, "warn"), size=size,
               weight=700 if i == 0 else 400, fill=WARN_INK, anchor="start",
               lang=lang)
        ty += size + 9
    y += box_h
    d.bottom = max(d.bottom, y)
    d.content_bottom = d.bottom  # everything but the footer band

    if d.content_bottom > FOOTER_TOP - FOOTER_CLEAR:
        raise LayoutError(
            f"content runs to y={d.content_bottom:.0f}, into the footer rule at "
            f"{FOOTER_TOP} ({'+'.join(langs)}) — shorten the copy, or take "
            f"some air out of the layout")

    # --- footer: the privacy promise, last thing on the page. Centred in the
    # band so a one-language poster does not leave it stranded at the top.
    rule(d, FOOTER_TOP)
    sizes = [15 if i == 0 else 13.5 for i in range(len(langs))]
    block = sum(size + 8 for size in sizes) - 8
    fy = FOOTER_TOP + (H - MARGIN / 2 - FOOTER_TOP - block) / 2 + sizes[0]
    for i, lang in enumerate(langs):
        d.text(W / 2, fy, s(i, "footer"), size=sizes[i],
               weight=600 if i == 0 else 400, fill=INK if i == 0 else MUTE,
               anchor="middle", lang=lang)
        fy += sizes[i] + 8

    return d


VARIANTS = {"": ("de", "en"), "-de": ("de",), "-en": ("en",)}


def main():
    # Single source of truth: the script writes BOTH the print master and the
    # site copy of every variant. Don't hand-edit the SVGs — re-run this.
    repo_root = pathlib.Path(__file__).resolve().parents[2]
    for suffix, langs in VARIANTS.items():
        svg = build(langs).svg()
        for target in (repo_root / "deploy" / "poster" / f"zeitspiegel-poster{suffix}.svg",
                       repo_root / "site" / f"poster{suffix}.svg"):
            target.write_text(svg + "\n")
            print("wrote", target.relative_to(repo_root))


if __name__ == "__main__":
    main()
