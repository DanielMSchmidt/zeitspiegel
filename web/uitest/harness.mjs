// Harness for the UI unit tests (docs/TESTPLAN.md §1.1, UI-1..UI-10).
//
// The page is served exactly as it ships — no bundling, no rewriting — and
// every /api/v1 call it makes is answered inside the browser's network layer
// by the stub fleet below. That keeps these tests about the page's own
// behaviour: what it renders, which unit a control talks to, and what it does
// when a unit stops answering. The API's behaviour is a separate concern with
// its own coverage (UT-23/24, IT-10, ST-8/12), so nothing here asserts
// anything about the server.
//
// Playwright is resolved through createRequire so a global install works
// (NODE_PATH) as well as a local one under web/uitest/node_modules.

import { createRequire } from "node:module";
import http from "node:http";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);

let chromium;
try {
  ({ chromium } = require("playwright"));
} catch (err) {
  throw new Error(
    "playwright not found. Install it locally (cd web/uitest && npm install " +
      "&& npx playwright install chromium) or point NODE_PATH at a global " +
      "install. Original error: " + err.message,
  );
}

const WEB_DIR = fileURLToPath(new URL("..", import.meta.url));

// A 1×1 PNG. The preview endpoint really serves multipart MJPEG; for the page
// all that matters is that <img> gets bytes it can decode instead of an error.
const PIXEL = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
  "base64",
);

const CORS = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET, PUT, POST, PATCH, OPTIONS",
  "Access-Control-Allow-Headers": "Content-Type",
};

// status builds a /api/v1/status body in the shape pinned by
// docs/REQUIREMENTS.md §3.
export function status(over = {}) {
  // `config` is pulled out and dropped here: a test declares a unit's config
  // alongside its status, but the Fleet owns it as mutable state and splices
  // a fresh snapshot into every answer.
  const { buffer, config: _config, ...rest } = over;
  return {
    delay_s: 0,
    fps: 30,
    resolution: "1920x1080",
    buffer: { capacity_s: 60, filled_s: 60, bytes: 12345, ...(buffer || {}) },
    dropped_frames: 0,
    min_latency_ms: 40,
    warming_up: false,
    uptime_s: 120,
    unit_id: "unit-a",
    name: "Corner",
    role: "primary",
    ...rest,
  };
}

// config builds a /api/v1/config body — the full config.Runtime, which is
// what both GET and PATCH answer with.
export function config(over = {}) {
  return {
    mirror_flip: false,
    profile: "auto",
    buffer_max_s: 60,
    focus_auto: true,
    focus_absolute: 0,
    exposure_auto: true,
    exposure_absolute: 0,
    ...over,
  };
}

// --- static file server ------------------------------------------------------

async function serveWeb() {
  const types = { ".html": "text/html; charset=utf-8", ".css": "text/css" };
  const server = http.createServer(async (req, res) => {
    let name = new URL(req.url, "http://localhost").pathname;
    if (name === "/") name = "/index.html";
    const file = path.join(WEB_DIR, path.normalize(name).replace(/^(\.\.[/\\])+/, ""));
    if (!file.startsWith(WEB_DIR)) {
      res.writeHead(403).end("forbidden");
      return;
    }
    try {
      const body = await fs.readFile(file);
      res.writeHead(200, { "Content-Type": types[path.extname(file)] || "application/octet-stream" });
      res.end(body);
    } catch {
      res.writeHead(404).end("not found");
    }
  });
  await new Promise((ok) => server.listen(0, "127.0.0.1", ok));
  return server;
}

// --- the stub fleet ----------------------------------------------------------

class Fleet {
  constructor(localOrigin, opts) {
    this.calls = [];
    this.units = new Map(); // origin -> unit
    this.local = {
      id: opts.local?.unit_id || "unit-a",
      base: "", // same origin as the page
      origin: localOrigin,
      status: status(opts.local || {}),
      config: config(opts.local?.config || {}),
      down: false,
      reject: null,
      clip: null,
      // slowRead holds every GET /status open for this many ms, which is how
      // a test lands a read that was made before a write but arrives after
      // it.
      slowRead: 0,
    };
    this.units.set(localOrigin, this.local);
    this.peers = [];
    this.nextAddr = 11;
    for (const p of opts.peers || []) this.addPeer(p);
  }

  // addPeer registers a mirror the host will list. base_url is a TEST-NET-1
  // address: a real cross-origin URL that can never leave the machine even if
  // a route ever failed to intercept it.
  addPeer(p) {
    const base = p.base_url || `http://192.0.2.${this.nextAddr++}:8080`;
    const unit = {
      id: p.id,
      base,
      origin: new URL(base).origin,
      status: status({ unit_id: p.id, name: p.name, role: "member", ...(p.status || {}) }),
      config: config(p.config || {}),
      down: p.down || false,
      reject: null,
      clip: null,
      slowRead: 0,
    };
    this.units.set(unit.origin, unit);
    this.peers.push(unit);
    return unit;
  }

  removePeer(id) {
    this.peers = this.peers.filter((u) => u.id !== id);
  }

  unit(id) {
    if (this.local.id === id) return this.local;
    return this.peers.find((u) => u.id === id);
  }

  peerList() {
    return { peers: this.peers.map((u) => ({ id: u.id, name: u.status.name, base_url: u.base, age_s: 1 })) };
  }

  // waitFor resolves once a matching call has been recorded, so tests never
  // have to guess how long a 1 Hz poll takes.
  async waitFor(pred, what = "a matching call", timeoutMs = 8000) {
    const deadline = Date.now() + timeoutMs;
    for (;;) {
      const hit = this.calls.find(pred);
      if (hit) return hit;
      if (Date.now() > deadline) {
        throw new Error(
          `timed out waiting for ${what}. Calls seen:\n` +
            this.calls.map((c) => `  ${c.method} ${c.url} ${c.body || ""}`).join("\n"),
        );
      }
      await new Promise((ok) => setTimeout(ok, 25));
    }
  }

  async handle(route) {
    const req = route.request();
    const url = new URL(req.url());
    const unit = this.units.get(url.origin);
    this.calls.push({
      method: req.method(),
      url: req.url(),
      origin: url.origin,
      path: url.pathname,
      params: url.searchParams,
      unit: unit ? unit.id : null,
      body: req.postData(),
    });

    if (req.method() === "OPTIONS") return route.fulfill({ status: 204, headers: CORS });
    // A mirror that was switched off: the connection simply fails, which is
    // what the page has to survive.
    if (!unit || unit.down) return route.abort("connectionrefused");

    const json = (body, code = 200) =>
      route.fulfill({
        status: code,
        headers: { ...CORS, "Content-Type": code >= 400 ? "application/problem+json" : "application/json" },
        body: JSON.stringify(body),
      });

    switch (url.pathname) {
      case "/api/v1/status": {
        // The config the unit is running under rides along with its status,
        // so a page watching a unit polls once (REQUIREMENTS §3).
        // The answer carries what the unit held when the read was made, not
        // when it was delivered — so a slow read arriving after a write
        // reports the value from before it. That is the stale reading a
        // polling page has to recognise and drop.
        const snapshot = { ...unit.status, config: { ...unit.config } };
        if (unit.slowRead) await new Promise((ok) => setTimeout(ok, unit.slowRead));
        return json(snapshot);
      }
      case "/api/v1/peers":
        return json(this.peerList());
      case "/api/v1/delay": {
        if (unit.reject) return json(unit.reject, 422);
        unit.status.delay_s = JSON.parse(req.postData() || "{}").seconds;
        return json({ delay_s: unit.status.delay_s });
      }
      case "/api/v1/config": {
        if (req.method() === "GET") return json(unit.config);
        // PATCH merges and answers with the whole config, the way the server
        // does (httpapi.patchConfig returns config.Runtime).
        Object.assign(unit.config, JSON.parse(req.postData() || "{}"));
        return json(unit.config);
      }
      case "/api/v1/preview":
        return route.fulfill({ status: 200, headers: { ...CORS, "Content-Type": "image/png" }, body: PIXEL });
      case "/api/v1/clip":
        if (unit.clip) return json(unit.clip, 503);
        // A real clip arrives as an attachment: the browser downloads it and
        // the hidden iframe never fires "load". Only the error path renders.
        return route.fulfill({
          status: 200,
          headers: {
            ...CORS,
            "Content-Type": "video/mp4",
            "Content-Disposition": 'attachment; filename="clip.mp4"',
            "X-Clip-Duration": "60.0",
          },
          body: "not really an mp4",
        });
      default:
        return json({ title: "not found" }, 404);
    }
  }
}

// --- entry point -------------------------------------------------------------

// openUI serves web/, opens a page in a phone-sized viewport and answers
// every API call from the stub fleet. Defaults to the control page;
// opts.path opens another one that ships in web/ (e.g. "/settings.html").
export async function openUI(opts = {}) {
  const server = await serveWeb();
  const origin = `http://127.0.0.1:${server.address().port}`;
  const browser = await chromium.launch();
  const context = await browser.newContext({
    viewport: opts.viewport || { width: 390, height: 844 },
    deviceScaleFactor: 2,
    hasTouch: true,
  });
  const page = await context.newPage();
  const fleet = new Fleet(origin, opts);
  await page.route("**/api/v1/**", (route) => fleet.handle(route));
  await page.goto(origin + (opts.path || "/"), { waitUntil: "domcontentloaded" });

  return {
    page,
    fleet,
    origin,
    cards: () => page.locator("#units .unit-card"),
    card: (id) => page.locator(`#units .unit-card[data-unit-id="${id}"]`),
    localCard: () => page.locator("#units .unit-card[data-local]"),
    async close() {
      await context.close();
      await browser.close();
      await new Promise((ok) => server.close(ok));
    },
  };
}

// setSlider moves a range input the way a finger does: the page listens for
// "input", and the pointer events are what tell it a drag is in progress.
export async function setSlider(locator, value, { hold = false } = {}) {
  await locator.evaluate((el, v) => {
    el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true }));
    el.value = String(v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  }, value);
  if (!hold) await releaseSlider(locator);
}

export async function releaseSlider(locator) {
  await locator.evaluate(() => window.dispatchEvent(new PointerEvent("pointerup", { bubbles: true })));
}
