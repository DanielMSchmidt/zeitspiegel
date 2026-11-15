# Printable "how to use" poster

A one-page A4 poster (vector, scales to any size) for the wall next to the
mirror. It reads in the order a guest needs it: what the screen in front of
them is *for*, then the three QR codes that get their phone the video and
the delay slider.

Three files, same layout, written by the same script:

| File | Languages |
|------|-----------|
| `zeitspiegel-poster.svg` | German with English underneath — the default |
| `zeitspiegel-poster-de.svg` | German only |
| `zeitspiegel-poster-en.svg` | English only |

## Print it

Open the SVG in any browser and print (it's sized A4 portrait, fits Letter
too). Or convert: `rsvg-convert -f pdf zeitspiegel-poster.svg > poster.pdf`.

The poster carries three QR codes: one that **joins the open Wi-Fi** on scan
(no password, E-7), one that **downloads the guest's video** straight to
their phone (`http://zeitspiegel.local/clip`, the quick-download page —
FR-19), and one that **opens the controls** where the delay is set
(`http://zeitspiegel.local`). They are numbered, and in that order — a phone
that scans code 2 or 3 first has nowhere to go, and the video is what most
guests came to the poster for.

## Regenerate / customize

```
python3 -m venv .venv && .venv/bin/pip install segno
.venv/bin/python make-poster.py        # or: make poster PYTHON=python3
```

One run writes all three variants, twice each: the print master here and the
site copy in `site/` (embedded by `site/guests.html`). Each pair is
byte-identical — this script is the single source of truth, so don't
hand-edit the SVGs. CI re-runs the generator and fails on any drift.

Environment overrides:

| Var | Default | Effect |
|-----|---------|--------|
| `URL` | `http://zeitspiegel.local` | what the controls QR opens; the download QR opens `URL`/clip |
| `IP` | `10.42.0.1` | always-works typed address (AP gateway) |
| `SSID` | `zeitspiegel` | Wi-Fi name shown (and encoded in the join QR) |
| `DELAY` | `25` | boot delay named on the poster (= `default_delay_s`) |

## Editing the copy

All the wording lives in `STRINGS` at the top of `make-poster.py`, one entry
per language. Every key must exist in both, and every key must be rendered;
the layout is a flow of measured, wrapped text, so a longer German line
pushes what follows down instead of overlapping it — and if the page fills
up, generation fails with a `LayoutError` rather than quietly printing
something that runs off the paper.

`make poster-test` (UT-30) checks all of that, plus that all three QR codes
still decode back to the network name and the URLs their captions promise —
on every variant, so no language edition can quietly lose one.
