# Printable "how to use" poster

`zeitspiegel-poster.svg` — a one-page A4 poster (vector, scales to any size)
explaining the three steps for guests: join the Wi-Fi → scan the QR →
set the delay / save clips. Tape it next to the mirror.

## Print it

Open the SVG in any browser and print (it's sized A4 portrait, fits Letter
too). Or convert: `rsvg-convert -f pdf zeitspiegel-poster.svg > poster.pdf`.

The poster carries two QR codes: one that **joins the open Wi-Fi** on scan
(no password) and one that **opens the controls** (`http://zeitspiegel.local`).

## Regenerate / customize

```
python3 -m venv .venv && .venv/bin/pip install segno
.venv/bin/python make-poster.py
```

Environment overrides:

| Var | Default | Effect |
|-----|---------|--------|
| `URL` | `http://zeitspiegel.local` | what the controls QR opens |
| `IP` | `10.42.0.1` | always-works typed address (AP gateway) |
| `SSID` | `zeitspiegel` | Wi-Fi name shown (and encoded in the join QR) |

Both QR codes are verified scannable (rendered to raster and decoded back).
