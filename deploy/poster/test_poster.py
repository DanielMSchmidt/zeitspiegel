#!/usr/bin/env python3
"""UT-26 — the printed poster, checked without printing it.

    python3 -m unittest discover -s deploy/poster        (or: make poster-test)

The poster is the only part of the product a guest reads before touching
anything, and it is generated, not drawn — so the things that can silently
break it are generation bugs: a translation that exists in one language but
not the other, a German line 20 % longer than its English twin running past
the margin, a block of copy pushing the footer off the page, a QR whose rects
no longer spell out what the caption promises. Each of those is asserted here.

Dependency: segno (same as the generator).
"""
import importlib.util
import pathlib
import re
import unittest

import segno

HERE = pathlib.Path(__file__).resolve().parent

_spec = importlib.util.spec_from_file_location("make_poster", HERE / "make-poster.py")
poster = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(poster)

VARIANTS = [("de", "en"), ("de",), ("en",)]


def build(langs=("de", "en"), **kw):
    return poster.build(langs, **kw)


class Translations(unittest.TestCase):
    # UT-26: neither language may quietly lag the other.
    def test_both_languages_carry_the_same_keys(self):
        de, en = set(poster.STRINGS["de"]), set(poster.STRINGS["en"])
        self.assertEqual(de - en, set(), "keys missing from the English strings")
        self.assertEqual(en - de, set(), "keys missing from the German strings")

    # UT-26: every string is actually placed on the bilingual poster — a
    # translated string nobody renders is as bad as a missing one.
    def test_every_string_is_rendered_in_both_languages(self):
        doc = build()
        placed = {(t["lang"], t["src"]) for t in doc.texts}
        for lang in ("de", "en"):
            for key, s in poster.strings(lang).items():
                self.assertIn((lang, s), placed, f"{lang}.{key} is never drawn")

    # UT-26: a single-language poster carries that language and nothing of
    # the other — the variants are for rooms that want one language only.
    def test_single_language_variants_carry_one_language(self):
        for lang, other in (("de", "en"), ("en", "de")):
            with self.subTest(lang=lang):
                langs = {t["lang"] for t in build((lang,)).texts}
                self.assertIn(lang, langs)
                self.assertNotIn(other, langs)


class Layout(unittest.TestCase):
    # UT-26: no line may run past the content column. German is the long
    # language; this is the assertion that catches it.
    def test_no_text_runs_past_the_margins(self):
        for langs in VARIANTS:
            doc = build(langs)
            for t in doc.texts:
                with self.subTest(langs=langs, text=t["s"]):
                    left, right = poster.extent(t)
                    self.assertGreaterEqual(round(left, 1), poster.MARGIN)
                    self.assertLessEqual(round(right, 1), poster.W - poster.MARGIN)

    # UT-26: everything drawn stays on the page, above the footer rule.
    def test_content_stays_on_the_page(self):
        for langs in VARIANTS:
            doc = build(langs)
            with self.subTest(langs=langs):
                self.assertLessEqual(doc.content_bottom, poster.FOOTER_TOP - poster.FOOTER_CLEAR)
                for t in doc.texts:
                    self.assertGreater(t["y"], poster.MARGIN / 2)
                    self.assertLess(t["y"], poster.H - poster.MARGIN / 4)

    # UT-26: the three-column rows (the step strip, the two QR captions) put
    # a German and an English label on one baseline. The German one is the
    # longer one, and nothing stops it growing into its neighbour but this.
    def test_columns_on_one_baseline_do_not_collide(self):
        cases = [(langs, {}) for langs in VARIANTS] + [
            (("de", "en"), {"ssid": "zeitspiegel-studio-nord",
                            "url": "http://zeitspiegel-studio-nord.local"})]
        for langs, kw in cases:
            rows = {}
            for t in build(langs, **kw).texts:
                rows.setdefault(round(t["y"], 1), []).append(t)
            for y, row in sorted(rows.items()):
                spans = sorted(poster.extent(t) for t in row)
                for (_, right), (left, _) in zip(spans, spans[1:]):
                    with self.subTest(langs=langs, y=y):
                        self.assertGreaterEqual(left - right, 12,
                                                f"columns touch at y={y}")

    # UT-26: the layout flows, so a longer network name or URL must push
    # things around rather than overlap them.
    def test_a_long_ssid_and_url_still_fit(self):
        for langs in VARIANTS:
            with self.subTest(langs=langs):
                doc = build(
                    langs,
                    ssid="zeitspiegel-studio-nord",
                    url="http://zeitspiegel-studio-nord.local",
                    ip="10.42.0.1",
                )
                self.assertLessEqual(doc.content_bottom, poster.FOOTER_TOP - poster.FOOTER_CLEAR)
                for t in doc.texts:
                    left, right = poster.extent(t)
                    self.assertGreaterEqual(round(left, 1), poster.MARGIN)
                    self.assertLessEqual(round(right, 1), poster.W - poster.MARGIN)

    # UT-26: wrapping is the mechanism the two assertions above rely on.
    def test_wrap_splits_only_where_it_must(self):
        self.assertEqual(poster.wrap("short line", 18, 400, 600), ["short line"])
        lines = poster.wrap("wort " * 40, 18, 400, 300)
        self.assertGreater(len(lines), 1)
        for line in lines:
            self.assertLessEqual(poster.text_width(line, 18, 400), 300)


class QRCodes(unittest.TestCase):
    """The two codes are the whole point of the poster: one joins the open
    Wi-Fi, the other opens the page with the delay slider."""

    def setUp(self):
        self.doc = build(ssid="zeitspiegel", url="http://zeitspiegel.local")

    # UT-26: the poster offers exactly the two codes, in order.
    def test_two_codes_join_then_controls(self):
        self.assertEqual(len(self.doc.qrs), 2)
        wifi, controls = self.doc.qrs
        self.assertEqual(wifi["payload"], "WIFI:S:zeitspiegel;T:nopass;;")
        self.assertEqual(controls["payload"], "http://zeitspiegel.local")
        self.assertLess(wifi["x"], controls["x"])
        self.assertEqual(wifi["size"], controls["size"])
        self.assertEqual(wifi["y"], controls["y"])

    # UT-26: read the emitted rects back as a module matrix and compare it
    # with what segno says the payload encodes. This is what proves the
    # run-length rects, the quiet zone and the scaling still spell out the
    # network name and the URL — a poster whose codes look fine and scan to
    # nothing is the one failure a human proofread never catches.
    def test_rects_decode_back_to_the_payload(self):
        svg = self.doc.svg()
        rects = [
            tuple(float(v) for v in m.groups())
            for m in re.finditer(
                r'<rect x="([\d.]+)" y="([\d.]+)" width="([\d.]+)" '
                r'height="([\d.]+)" fill="#000"/>',
                svg,
            )
        ]
        self.assertTrue(rects, "no QR modules were emitted")
        for qr in self.doc.qrs:
            with self.subTest(payload=qr["payload"]):
                want = [list(row) for row in segno.make(qr["payload"], error="m").matrix]
                self.assertEqual(read_matrix(rects, qr, len(want)), want)


def read_matrix(rects, qr, n):
    """Sample the emitted black rects at each module centre of one QR box."""
    m = qr["size"] / (n + 2 * qr["quiet"])
    out = []
    for r in range(n):
        row = []
        for c in range(n):
            cx = qr["x"] + (c + qr["quiet"]) * m + m / 2
            cy = qr["y"] + (r + qr["quiet"]) * m + m / 2
            row.append(int(any(
                x <= cx <= x + w and y <= cy <= y + h for x, y, w, h in rects
            )))
        out.append(row)
    return out


if __name__ == "__main__":
    unittest.main()
