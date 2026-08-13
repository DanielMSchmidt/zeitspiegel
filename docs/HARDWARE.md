# docs/HARDWARE.md

# Camera Selection

Nothing on this page has been validated on real hardware yet. Spike S-2
(docs/TESTPLAN.md §3) is what turns a candidate into *the* camera; until its
numbers land in ARCHITECTURE.md §7, every recommendation here is provisional
and derived from vendor datasheets.

## 1. Hard constraints

A camera that fails any of these does not work at all — these are not
preferences.

| Constraint | Why | Where |
|---|---|---|
| **USB UVC** | The adapter is V4L2 over go4vl. CSI cameras are designed out (`camera_auto_detect=0`) | `deploy/setup.sh` |
| **Native MJPEG** | The ring buffer stores the camera's own JPEGs. There is no YUYV path and no live encoder — a camera without MJPEG cannot be opened at all | D2, `camera.go` `PixelFmtMJPEG` |
| **A discrete MJPEG mode ≤ 1920×1080** | `selectMode` rejects everything above the `config.MaxAuto{Width,Height}` cap | E-2, `camera/modes.go` |
| **Bus-powered from the Pi** | Which is why the 5 V/5 A PSU is mandatory | docs/DEPLOYMENT.md |

Two constraints that used to exist and no longer do: the camera needed to
implement `focus_auto`/`focus_absolute`, and its aspect ratio had to match the
panel. Unsupported controls are now skipped rather than fatal (E-9), and the
renderer letterboxes (FR-16).

**Verify on arrival, do not trust the listing.** Vendor and marketplace specs
are wrong often enough that this is the real gate:

```
v4l2-ctl --list-formats-ext   # want: [MJPG] 1920x1080 @ 30 fps, discrete
v4l2-ctl --list-ctrls         # records which controls exist; absent focus
                              # controls are expected on a fixed-focus camera
```

## 2. Field of view

Wider is better here, up to a point. Coverage and subject size at 1080p:

| dFOV | hFOV | width @ 2 m | width @ 3 m | 1.8 m dancer @ 3 m |
|---|---|---|---|---|
| 78° | 70° | 2.8 m | 4.2 m | 817 px tall (76% of frame) |
| 90° | 82° | 3.5 m | 5.2 m | 661 px (61%) |
| 100° | 92° | 4.2 m | 6.2 m | 555 px (51%) |
| 120° | 113° | 6.0 m | 9.1 m | 381 px (35%) |

**For:** more room covered, shorter working distance, more dancers in frame,
and much deeper depth of field.

**Against:** fewer pixels on the dancer. E-2 chose 1080p30 over 720p60 because
dancers read the screen from across a room; going ultra-wide spends that gain.
Past ~120° barrel distortion becomes visible and curves body lines, which
matters in a mirror — nothing in the pipeline corrects distortion.

**Target 100-110° diagonal.** Mount a 120° camera closer (~2 m) to land in the
same place.

## 3. Fixed focus is correct, not a compromise

A fixed-focus lens is set at its *hyperfocal distance*, so depth of field runs
from roughly half that distance to infinity. Short focal length plus a small
sensor makes that distance tiny:

| Setup | focal length | hyperfocal | sharp range |
|---|---|---|---|
| 78° dFOV, 1/2.8″, f/2.0 | ~4.0 mm | ~1.83 m | 0.92 m → ∞ |
| 100° dFOV, 1/2.8″, f/2.0 | ~2.7 mm | ~0.85 m | **0.42 m → ∞** |
| 100° dFOV, 1/3″, f/2.8 | ~2.5 mm | ~0.57 m | **0.28 m → ∞** |

The "30 cm" figure on fixed-focus spec sheets is the **near** limit, not a far
limit — everything beyond it, out to infinity, is sharp. Wider FOV makes this
better, so wide-angle plus fixed focus is exactly what "everything in view is
clear" requires.

Autofocus cameras reach the same place by a different route: pin them with
`focus_auto = false` and a measured `focus_absolute`. That works, but it is one
more thing to measure and one more thing that can drift.

## 4. Do not buy 4K

The display is not the constraint — a Pi 5 drives 4Kp60 on HDMI0. Everything
downstream of it is:

- **The pipeline caps at 1080p.** `config.MaxAuto{Width,Height}`, per E-2.
- **Decode is software.** Every frame goes through SDL2_image/libjpeg-turbo
  against a 16.7 ms tick. 4K UHD is 9.0× the pixels of 720p — roughly
  36-72 ms/frame, 2-4× over budget. 1080p at 2.25× is already tight.
- **Memory.** The 60 s / 1 GiB budget assumes bright-scene 1080p30 MJPEG
  spiking to ~15 MB/s. 4K is ~4× that; the buffer would not fit and
  `GOMEMLIMIT=1400MiB` would be blown.
- **Export.** The Pi 5 has no hardware H.264 encoder (D4) and export already
  downscales to a 720p long edge — 4K would be decoded only to be discarded.

A 4K camera is not *fatal* — `selectMode` would select its 1080p MJPEG mode,
and a downsampled 4K sensor can look marginally better. But you would pay for
resolution that is unreachable by design, and the 4K webcam market is
overwhelmingly the AI-processing class below.

## 5. Do not buy an "AI" webcam

Auto-framing, background blur/bokeh, beauty and skin-smoothing modes are
disqualifying:

- **Auto-framing pans and crops the frame as the dancer moves.** A mirror whose
  framing chases its subject is useless for judging position in a room.
- **Background blur is the literal opposite of the requirement.**
- **Aggressive temporal noise reduction smears motion**, which is what a delay
  mirror exists to show.
- **You cannot turn them off from here.** These are configured by Windows- and
  macOS-only vendor software. Over plain UVC on Linux you inherit whatever
  firmware state was last saved, with no way to change it.

Excluded by name: OBSBOT Meet series, Insta360 Link / Link 2C, Razer Kiyo V2,
WyreStorm FOCUS 210, Logitech Brio 500 (its "Point Mode" is auto-framing),
and anything advertising AI tracking or auto-framing.

This is also why the **Razer Kiyo is no longer the reference camera** — it is
discontinued, and its successor is exactly this class.

## 6. Shortlist

Two form factors, and they fail in opposite directions.

A **stock webcam** is a finished product: moulded body, hinged clip that sits on
top of a TV, tripod thread, cable strain relief. A **camera module** is a bare
PCB — you supply the housing, the mount and the strain relief. §6.1 covers the
first, §6.2 the second.

The trade-off is not cosmetic:

|  | Stock webcam | Camera module |
|---|---|---|
| MJPEG at 1080p30 | effectively guaranteed¹ | must be checked per device |
| dFOV | 78-120°, mostly 78-90° | 100-130°, freely chosen |
| Sensor size | rarely published | published, and larger |
| Focus | autofocus (drifts) or manual | fixed at hyperfocal — §3 |
| Mounting on a TV | it is the product | your problem |
| §5 "AI" risk | high, and rising | nil |

¹ USB 2.0 carries 480 Mbit/s. Uncompressed 1080p30 YUYV needs ~995 Mbit/s. So
any camera advertising **USB 2.0 and 1080p30 is compressing** — in practice
MJPEG, since that is what UVC mandates. This is why the MJPEG column is a
non-issue for §6.1 and the main risk for §6.2. It does not hold for USB 3.0
cameras, which is one more reason to prefer USB 2.0 parts.

**Owner preference: §6.1.** The mirror is a TV with a camera on top of it, and
the overhead of housing and mounting a bare PCB is not wanted. §6.2 is kept
because it is where the sensor quality and the FOV actually are — if §7 turns
out to bite, that is where the answer lives.

Ranked by how much of the above is *documented* rather than assumed. None is
hardware-tested. Confidence in the MJPEG column:

- ✅✅ — a buyer reports the MJPEG mode working over V4L2 on Linux
- ✅ — the manufacturer publishes it, or footnote ¹ forces it
- ⚠️ — marketplace copy only, or the format is not stated at all

Prices and stock verified on amazon.de 2026-08-11, delivering to Hamburg.

### 6.1 Stock webcams — preferred

| # | Camera | dFOV | Focus | MJPEG 1080p30 | Price | Where |
|---|---|---|---|---|---|---|
| W1 | **LogiLink UA0378** conference | **100°** | manual | ✅ | €33.99 | [amazon.de B08QCXMC1V](https://www.amazon.de/dp/B08QCXMC1V) |
| W2 | **LogiLink UA0377** conference | 120° | manual | ✅ | €44.11 | [amazon.de B08QD7QDTJ](https://www.amazon.de/dp/B08QD7QDTJ) |
| W3 | eMeet C960 | 90° | fixed ⚠️ | ✅ | €29.99 | [amazon.de B0002HAHUY](https://www.amazon.de/dp/B0002HAHUY) |
| W4 | Logitech C920s HD Pro | 78° | autofocus, pin it | ✅ | €55.18 | [amazon.de B07MM4V7NR](https://www.amazon.de/dp/B07MM4V7NR) |

- **W1 is the recommended buy.** 100° is exactly the E-9 target; f/2.2; manual
  focus, which on a static mirror is strictly better than autofocus — you set
  it once at the dancing distance and it cannot hunt or drift. Explicitly lists
  Linux. Swivels −90°/+0° and rotates ±180°, clips to an LCD screen, and has a
  tripod thread. USB 2.0 at 1080p30, so footnote ¹ applies. Every "automatic"
  feature it advertises (AE, AWB, flicker, gamma, edge enhancement) is
  ordinary sensor-side UVC processing, not §5 framing. Caveat: 12 ratings, and
  the sensor size is not published anywhere — §7 is unquantified for it.
- **W2** is the same camera family at 120° and f/2.4. Take it only if the room
  forces a ~2 m mount; otherwise W1's 100° is the better shape.
- **W3** is the volume choice at 5,101 ratings, with a working tripod thread
  and 180°/90° rotation, but 90° is below target and the listing never states
  the focus type. eMeet's autofocus model is the separate NOVA, which implies
  C960 is fixed — implication, not documentation.
- **W4** is the most-proven UVC device on this page by an order of magnitude
  (10,706 ratings) and will certainly work. 78° is simply too narrow: per §2 it
  covers 2.8 m at 2 m, and per §3 its near-sharp limit is around 0.9 m. Buy it
  only as a known-good control device for debugging the pipeline.

### 6.2 Camera modules — bare PCB, better optics

| # | Camera | dFOV | Focus | Sensor | MJPEG 1080p30 | Price | Where |
|---|---|---|---|---|---|---|---|
| M1 | **Arducam IMX291**, waterproof metal housing | 120° | fixed | 1/2.8″, 0.001 lux, 80 dB WDR | ✅✅ | €55.00 | [amazon.de B0C36ZVQ5G](https://www.amazon.de/dp/B0C36ZVQ5G) |
| M2 | **ELP 8MP**, 105°, with mic | 105° | fixed | 1/3.2″ IMX179 | ✅ | €43.99 | [amazon.de B07DBYKG7X](https://www.amazon.de/dp/B07DBYKG7X) |
| M3 | **innomaker 121°**, true HDR/WDR | 121° | manual, lockable | PS5268, 120 dB HDR | ✅✅ | €29.99 | [amazon.de B0H26BG9RP](https://www.amazon.de/dp/B0H26BG9RP) |
| M4 | Svpro 8MP, 102° | 102° | fixed | 1/3.2″ IMX179 | ⚠️ | €42.99 | [amazon.de B0F6LK8MKH](https://www.amazon.de/dp/B0F6LK8MKH) |
| M5 | WyreStorm FOCUS 100 | 100° | fixed | 1/3″, F2.8 | ✅ | — | AV distribution; no Amazon.de listing found |
| M6 | innomaker 130° | 130° | manual | — | ✅ | €18.99 | [amazon.de B0CNCSFQC1](https://www.amazon.de/dp/B0CNCSFQC1) |

Trade-offs worth knowing:

- **M1** has the best sensor here by a clear margin, which matters because §7
  is the unmeasured risk. Its MJPEG mode is printed in the listing *and* a
  verified buyer reports "supports H.264 & MJPEG simultaneously, at 1080p30"
  under V4L. It is a cased module with a threaded yoke rather than a clip-on
  webcam — which suits bolting to a TV. At 120° mount it around 2 m. Two
  cautions: several reviewers report the yoke set screws are too long to lock
  the angle (a set of 4 mm fine-thread M2 screws fixes it), and the "Product
  description" block is copy-pasted from a different product (8MP IMX179, 115°
  *manual* focus) and contradicts the bullets. Trust the bullets.
- **M2** is the best FOV match that is actually orderable — 105° sits dead
  centre of the target — and ELP publishes the full mode table. The 1/3.2″
  sensor is the cost.
- **M3** is the only candidate with a first-hand report of *this* pipeline: a
  reviewer runs it on a Raspberry Pi under Klipper/crowsnest at a stable
  1080p30 MJPEG stream. Its 120 dB hardware HDR is the best answer on this list
  to a studio with a bright window and dark corners — but two reviewers say the
  low-light marketing is overstated and it noises up quickly, and sensor noise
  inflates MJPEG bitrate, which is a §7 problem. Focus is manual, not fixed:
  set it once and threadlock it.
- **M4** is the only in-stock listing that states fixed focus outright, at a
  near-ideal 102°. But MJPEG is not stated anywhere on the listing. Svpro's own
  comparison table shows Mjpeg/YUY2 across the range so it is very likely
  present — this is the one where "verify on arrival" carries the most weight.
- **M5** is the best pure spec match at exactly 100°, but its 1/3″ F2.8 sensor
  is the weakest here in a dim studio, and it is not an Amazon purchase.
- **M6** is a €19 "does the pipeline work end to end" unit, not a candidate for
  the finished mirror. 130° is past the point where barrel distortion curves
  body lines, and nothing downstream corrects it (§2).

Note that no module implements `focus_absolute`, so **nothing in §6.2 exercises
the FR-9 focus-control path** — that path is only covered if a §6.1 autofocus
webcam (W4) is also on hand. Worth one such device in the drawer regardless of
which camera ships.

**M2 and M4 are 8MP sensors**, and §8 is what makes them safe to buy. Their
headline modes are 3264×2448 at 15 fps — under the 25 fps floor, so `auto`
passes over them and opens 1920×1080@30 instead. Nothing needs pinning. Buying
a sensor whose top mode the pipeline will refuse is now a non-event rather than
a trap, which is why the 8MP options are ranked on their optics above.

Listings churn: four cameras this page once recommended have since been
delisted or drifted to a different product. Re-check availability before
ordering, and re-verify against §1 before adding anything here.

## 7. Low light is the real risk

The Kiyo had a ring light; none of these do. A dim studio at 30 fps forces high
gain, and sensor noise is expensive to JPEG-compress — so poor lighting inflates
MJPEG bitrate, which eats the buffer budget directly. S-2 must measure
`bytes_per_s` and `max_frame_bytes` under actual studio lighting before
`buffer_max_bytes` is trusted. Prefer the largest sensor you can get (1/2.8″
over 1/3″), and light the room.

## 8. What `auto` actually picks

`auto` reads the frame rate of **every** MJPEG mode the camera advertises and
opens the largest one that clears a **25 fps floor**, capped at 1080p. Frame
rate is the constraint; resolution is maximised under it. So:

| Camera offers | `auto` opens | Why |
|---|---|---|
| 1920×1080@30, 1280×720@60 | **1920×1080@30** | both clear the floor, so pixels win (E-2) |
| 1600×1200@15, 1280×720@30 | **1280×720@30** | trades resolution to hold the rate |
| 1920×1080@25, 1280×720@30 | **1920×1080@25** | 25 clears the floor, so keep the pixels |
| 1920×1080@24, 1280×720@30 | **1280×720@30** | just under the floor, so the rate wins |
| 1920×1080@60 only | **1920×1080@60** | 60 is fine, and is carried downstream |
| 1920×1080@15, 640×480@20 | **640×480@20** | nothing clears the floor; fastest wins |

The floor is 25 rather than a hair under 30 on purpose: it absorbs NTSC and PAL
rates (29.97 and 25) and buys resolution, because holding 1080p at 25 fps beats
dropping to 720p to gain five frames. Below 25, motion starts reading as
stutter — which defeats the point of a movement mirror.

It never refuses a camera for being slow — a choppy mirror beats a black
screen.

The mode it chose is in the boot log (`source opened … width=… height=… fps=…`)
and in `GET /api/v1/status`, which reports the real mode rather than the profile
nominal. Check there first if the picture looks softer or choppier than
expected, and compare against `v4l2-ctl --list-formats-ext`.

To override, set `profile = "1080p30"` or `"720p60"` explicitly instead of
`auto`.

**One budget note:** if your camera's best 30 fps mode is 1080p**60**, `auto`
takes it and the byte rate roughly doubles. `buffer_max_bytes` (1 GiB) then
evicts before `buffer_max_s` (60 s), so the usable delay window shrinks. That is
correct ring-buffer behaviour, not a bug — pin `1080p30` if you would rather
have the full 60 s.
