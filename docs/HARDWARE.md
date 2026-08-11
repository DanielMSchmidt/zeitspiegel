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
| **A discrete MJPEG mode ≤ 1920×1080** | `probeMaxMJPEG` rejects everything above the `config.MaxAuto{Width,Height}` cap | E-2, `camera.go` |
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

A 4K camera is not *fatal* — `probeMaxMJPEG` would select its 1080p MJPEG mode,
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
WyreStorm FOCUS 210, and anything advertising AI tracking or auto-framing.

This is also why the **Razer Kiyo is no longer the reference camera** — it is
discontinued, and its successor is exactly this class.

## 6. Shortlist

Ranked by how much of the above is *documented* rather than assumed. None is
hardware-tested; ✅ means the manufacturer publishes it, ⚠️ means the claim
comes from marketplace copy.

| # | Camera | dFOV | Focus | Sensor | MJPEG 1080p30 | Where |
|---|---|---|---|---|---|---|
| 1 | **Arducam IMX291**, waterproof metal housing | 120° | fixed | 1/2.8″, 0.001 lux | ✅ | [amazon.de B0C36ZVQ5G](https://www.amazon.de/Arducam-USB-Kamera-Weitwinkel-Kameramodule-wasserdichtem-Metallgeh%C3%A4use/dp/B0C36ZVQ5G) |
| 2 | **WyreStorm FOCUS 100** | 100° | fixed | 1/3″, F2.8 | ✅ | AV distribution; no Amazon.de listing found |
| 3 | Arducam IMX291 module | 100-120°¹ | fixed | 1/2.8″ | ✅ | [amazon.de B07ZS7LX3Y](https://www.amazon.de/Arducam-Kamera-Modul-Computer-Weitwinkel-Mikrofon/dp/B07ZS7LX3Y) |
| 4 | Logitech C930e | 90° | autofocus, pin it | 1/3″ | ✅ | widely stocked, ~€44 |
| 5 | Spedal, manual focus 7 cm→∞ | 120° | manual, lockable | — | ⚠️ | [amazon.de B07TF5J6JZ](https://www.amazon.de/Computer-Kamera-Streaming-Weitwinkel-Webcam-Videoanruf-Videokonferenzen/dp/B07TF5J6JZ) |
| 6 | Spedal 100° | 100° | fixed | — | ⚠️ | [amazon.de B07N2JQMY2](https://www.amazon.de/Spedal-Weitwinkel-Streaming-Kamera-PC-Desktop-Laptop-kompatibel/dp/B07N2JQMY2) |
| 7 | Svpro IMX214, metal housing | 100° | fixed | 1/3.06″ | ⚠️ | [amazon.de B09D7MTL57](https://www.amazon.de/Svpro-Weitwinkel-Fixfokus-100Degree-Metallgeh%C3%A4use/dp/B09D7MTL57) |

¹ The German listing does not clearly distinguish the 100° from the 120°
variant; check the title and photo before ordering.

Trade-offs worth knowing:

- **#1** is the only readily-orderable option meeting every hard constraint on
  paper, and its 1/2.8″ sensor is the best low-light performer on the list. It
  is a cased module with a threaded mount rather than a clip-on webcam — which
  suits bolting to a TV. At 120° mount it around 2 m.
- **#2** is the best pure spec match at exactly 100°, but its 1/3″ F2.8 sensor
  is the weakest here in a dim studio, and it is not an Amazon purchase.
- **#4** is the boring safe choice: unquestionably works on Linux, but 90° is
  below target and it needs its focus pinned.
- **#5-7** are the right shape at the right price, but MJPEG is unverified.
  If one of them only offers YUYV it will not open at all. Buy from somewhere
  with easy returns and run `v4l2-ctl` first.

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
