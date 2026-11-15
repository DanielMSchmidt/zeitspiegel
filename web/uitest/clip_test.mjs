// UI unit tests for the guest quick-download page (/clip, FR-19) — the page
// the poster's download QR opens. Run with `make test-ui`; the API is
// stubbed the same way as for the control page (docs/TESTPLAN.md §1.1).

import test from "node:test";
import assert from "node:assert/strict";
import { openUI } from "./harness.mjs";

const sleep = (ms) => new Promise((ok) => setTimeout(ok, ms));

async function until(fn, what, timeoutMs = 10000) {
  const deadline = Date.now() + timeoutMs;
  let last = null;
  for (;;) {
    try {
      if ((await fn()) !== false) return;
    } catch (err) {
      last = err;
    }
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${what}` + (last ? `\nlast error: ${last.message}` : ""));
    }
    await sleep(50);
  }
}

function clipCalls(fleet, unit) {
  return fleet.calls.filter((c) => c.path === "/api/v1/clip" && c.unit === unit);
}

// UI-16: scanning the QR is the whole interaction — the download starts by
// itself, for the serving mirror's full buffer, and the one button repeats
// it without re-reading anything.
test("UI-16 the page starts the download by itself and the button repeats it", async (t) => {
  const ui = await openUI({
    path: "/clip",
    local: { unit_id: "unit-a", name: "Corner", buffer: { capacity_s: 45, filled_s: 45 } },
  });
  t.after(() => ui.close());

  const first = await ui.fleet.waitFor(
    (c) => c.path === "/api/v1/clip" && c.unit === "unit-a",
    "the automatic download",
  );
  assert.equal(first.params.get("seconds"), "45", "the length is that mirror's own capacity");
  assert.equal(first.params.get("format"), "mp4", "phones get the phone-playable format");
  assert.equal(first.origin, ui.origin, "the download comes from the mirror that served the page");

  // The serving mirror is named, so with several mirrors in the room the
  // guest can tell whose video this is.
  await until(
    async () => (await ui.page.locator("#mirror-name").textContent()) === "Corner",
    "the serving mirror's name",
  );

  const btn = ui.page.locator("#download-again");
  await until(async () => await btn.isEnabled(), "the button to free up after the grace period");
  await btn.click();
  await until(
    () => clipCalls(ui.fleet, "unit-a").length >= 2,
    "a second download after the click",
  );
  const second = clipCalls(ui.fleet, "unit-a")[1];
  assert.equal(second.params.get("seconds"), "45", "the repeat asks for the same clip");
});

// UI-17: one export slot serves the whole room, so after a dance several
// phones scan at once and all but one are told 503. The page treats that as
// "wait your turn": it retries by itself, bounded, and stops once a download
// went through.
test("UI-17 a busy mirror is retried automatically until the download starts", async (t) => {
  const ui = await openUI({
    path: "/clip",
    local: {
      unit_id: "unit-a",
      name: "Corner",
      clip: { type: "about:blank", title: "export slots busy", status: 503,
              detail: "export: all export slots busy" },
    },
  });
  t.after(() => ui.close());

  await ui.fleet.waitFor((c) => c.path === "/api/v1/clip", "the automatic first attempt");
  await until(
    async () => /busy/i.test(await ui.page.locator("#dl-status").textContent()),
    "the page to say the mirror is busy",
  );

  // No click anywhere: the page retries on its own.
  await until(() => clipCalls(ui.fleet, "unit-a").length >= 2, "an automatic retry");

  // The slot frees up; the next retry succeeds and the page says so.
  ui.fleet.local.clip = null;
  await until(
    async () => /started/i.test(await ui.page.locator("#dl-status").textContent()),
    "the page to report the download started",
  );
  assert.equal(await ui.page.locator("#download-again").isEnabled(), true);

  // And having succeeded, it stops asking — a page left open must not keep
  // exporting clips forever.
  const settled = clipCalls(ui.fleet, "unit-a").length;
  await sleep(3200);
  assert.equal(clipCalls(ui.fleet, "unit-a").length, settled, "no further downloads after success");
});

// UI-18: with several mirrors in the room the page still downloads from the
// serving one by itself, but names the others — and each of their buttons
// asks that mirror directly, at its own address and for its own buffer
// length, never proxied through the host (the FR-14 discipline applied to
// downloads).
test("UI-18 other mirrors get their own direct download buttons", async (t) => {
  const ui = await openUI({
    path: "/clip",
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [
      { id: "unit-b", name: "Barre", status: { buffer: { capacity_s: 30, filled_s: 30 } } },
    ],
  });
  t.after(() => ui.close());

  // The automatic download is the serving mirror's and only the serving
  // mirror's.
  await ui.fleet.waitFor((c) => c.path === "/api/v1/clip" && c.unit === "unit-a", "the automatic download");
  assert.equal(clipCalls(ui.fleet, "unit-b").length, 0, "no unasked-for download from the peer");

  const peerBtn = ui.page.locator("#peer-buttons button", { hasText: "Barre" });
  await until(async () => (await peerBtn.count()) === 1, "a button for the other mirror");
  await peerBtn.click();

  const peerClip = await ui.fleet.waitFor(
    (c) => c.path === "/api/v1/clip" && c.unit === "unit-b",
    "a download from the other mirror",
  );
  assert.equal(peerClip.params.get("seconds"), "30", "that mirror's own buffer length");
  assert.notEqual(peerClip.origin, ui.origin, "asked at its own address, not through the host");
  assert.equal(clipCalls(ui.fleet, "unit-a").length, 1, "the serving mirror was not asked again");
});
