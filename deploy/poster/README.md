# Printable "how to use" poster

`zeitspiegel-poster.svg` — a one-page A4 poster (vector, scales to any size)
explaining the three steps for guests: join the Wi-Fi → scan the QR →
set the delay / save clips. Tape it next to the mirror.

## Print it

Open the SVG in any browser and print (it's sized A4 portrait, fits Letter
too). Or convert: `rsvg-convert -f pdf zeitspiegel-poster.svg > poster.pdf`.

The committed poster is generic: SSID `zeitspiegel`, a blank line to write
the password, and a QR to `http://zeitspiegel.local`.

## Regenerate / customize

```
python3 -m venv .venv && .venv/bin/pip install segno
.venv/bin/python make-poster.py
```

Environment overrides:

| Var | Default | Effect |
|-----|---------|--------|
| `URL` | `http://zeitspiegel.local` | what the main QR opens |
| `SSID` | `zeitspiegel` | Wi-Fi name shown |
| `WIFI_PASS` | _(blank line)_ | if set, prints the password **and** adds a second QR that joins the Wi-Fi on scan (WPA) |

Example, a ready-to-hang poster for one appliance (password from
`build/credentials.txt`):

```
WIFI_PASS=zie5vtlb05zm .venv/bin/python make-poster.py
```

Both QR codes are verified scannable (rendered to raster and decoded back).
