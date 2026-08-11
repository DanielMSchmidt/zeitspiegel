// UI unit tests — the control page's own behaviour, with the API stubbed
// (docs/TESTPLAN.md §1.1). Run with `make test-ui`.
//
// What these cover is the half no Go test can see: that a card exists per
// mirror, that a control inside a card talks to that mirror and no other,
// and that the page survives a mirror that stops answering. What the server
// does with those calls is covered by UT-23/24, IT-10 and ST-8/12.

import test from "node:test";
import assert from "node:assert/strict";
import { openUI, setSlider, releaseSlider } from "./harness.mjs";

const sleep = (ms) => new Promise((ok) => setTimeout(ok, ms));

// until polls an assertion until it holds, so tests never hard-code how long
// a 1 Hz poll takes to come round.
async function until(fn, what, timeoutMs = 8000) {
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

const CONTROLS = [".unit-delay", ".unit-preview-toggle", ".unit-preview-view", ".unit-format", ".unit-download"];

// UI-1: a lone mirror is one card carrying everything you can do to it.
test("UI-1 a lone mirror renders a single card with delay, preview and clip controls", async (t) => {
  const ui = await openUI({ local: { name: "Corner" } });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 1, "one card");
  const card = ui.localCard();
  await until(async () => (await card.locator(".unit-name").textContent()) === "Corner", "the mirror's own name");

  for (const sel of CONTROLS) {
    assert.equal(await card.locator(sel).count(), 1, `card is missing ${sel}`);
  }
  assert.equal(await ui.page.locator("#fleet-hint").isVisible(), false, "no fleet hint when alone");
});

// UI-2: one card per mirror, this one first, and the list follows the fleet.
test("UI-2 a card per connected mirror, tracking mirrors joining and leaving", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre" }, { id: "unit-c", name: "Window" }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 3, "three cards");
  assert.deepEqual(await ui.cards().locator(".unit-name").allTextContents(), ["Corner", "Barre", "Window"]);
  assert.notEqual(await ui.cards().nth(0).getAttribute("data-local"), null, "this mirror comes first");
  assert.equal(await ui.page.locator("#fleet-hint").isVisible(), true, "fleet hint shown with company");

  // A mirror is switched off; its card goes with it.
  ui.fleet.removePeer("unit-b");
  await until(async () => (await ui.cards().count()) === 2, "the card of a departed mirror to go");
  assert.deepEqual(await ui.cards().locator(".unit-name").allTextContents(), ["Corner", "Window"]);

  // And plugged back in, on a fresh address.
  ui.fleet.addPeer({ id: "unit-b", name: "Barre" });
  await until(async () => (await ui.cards().count()) === 3, "the returning mirror to get a card");
  await until(
    async () => (await ui.card("unit-b").locator(".unit-delay").count()) === 1,
    "the rebuilt card to carry its own slider",
  );
});

// UI-3: FR-14 as the page sees it — a card's slider moves that mirror only.
test("UI-3 each card's delay slider PUTs to its own mirror and no other", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre" }, { id: "unit-c", name: "Window" }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 3, "three cards");

  await setSlider(ui.card("unit-b").locator(".unit-delay"), 12);
  const put = await ui.fleet.waitFor(
    (c) => c.method === "PUT" && c.path === "/api/v1/delay" && c.unit === "unit-b",
    "a delay PUT to unit-b",
  );
  assert.equal(JSON.parse(put.body).seconds, 12);
  assert.equal(put.origin, ui.fleet.unit("unit-b").base, "the mirror is addressed directly, not proxied");

  const strays = ui.fleet.calls.filter((c) => c.method === "PUT" && c.unit !== "unit-b");
  assert.deepEqual(strays, [], "moving one mirror's slider must not touch another");

  // This mirror's own card posts to the page's origin.
  await setSlider(ui.localCard().locator(".unit-delay"), 3);
  const own = await ui.fleet.waitFor(
    (c) => c.method === "PUT" && c.path === "/api/v1/delay" && c.unit === "unit-a",
    "a delay PUT to this mirror",
  );
  assert.equal(own.origin, ui.origin);
  assert.equal(JSON.parse(own.body).seconds, 3);
});

// UI-4: the poll must not yank a slider out from under a finger.
test("UI-4 a slider being dragged is not overwritten by the status poll", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre" }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 2, "two cards");
  const slider = ui.card("unit-b").locator(".unit-delay");

  await setSlider(slider, 5, { hold: true });
  await ui.fleet.waitFor((c) => c.method === "PUT" && c.unit === "unit-b", "the debounced PUT");

  // Someone else moves that mirror while the finger is still down.
  ui.fleet.unit("unit-b").status.delay_s = 30;
  await sleep(1200); // at least one poll
  assert.equal(await slider.inputValue(), "5", "the poll grabbed the slider mid-drag");
  assert.equal(await ui.card("unit-b").locator(".unit-delay-value").textContent(), "5.0");

  // Once the finger is up and the hold-off has passed, the mirror wins again.
  await releaseSlider(slider);
  await until(async () => (await slider.inputValue()) === "30", "the slider to follow the mirror again");
});

// UI-5: a mirror that stops answering stays visible but obviously inert.
test("UI-5 an unreachable mirror is marked offline and recovers on its own", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre" }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 2, "two cards");

  ui.fleet.unit("unit-b").down = true;
  await until(
    async () => (await ui.card("unit-b").locator(".unit-badge").textContent()) === "Offline",
    "the offline badge",
  );
  assert.match(await ui.card("unit-b").getAttribute("class"), /offline/);

  ui.fleet.unit("unit-b").down = false;
  await until(async () => !(await ui.card("unit-b").locator(".unit-badge").isVisible()), "the badge to clear");

  // The same has to hold for the mirror serving the page: with the header
  // badges gone, its own card is what reports it.
  ui.fleet.unit("unit-a").down = true;
  await until(
    async () => (await ui.localCard().locator(".unit-badge").textContent()) === "Offline",
    "this mirror's own offline badge",
  );
});

// UI-6: previews are per card and independent.
test("UI-6 a card's preview streams from that mirror only", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre" }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 2, "two cards");
  const peer = ui.card("unit-b");
  const base = ui.fleet.unit("unit-b").base;

  await peer.locator(".unit-preview-toggle").click();
  await until(
    async () => (await peer.locator(".unit-preview").getAttribute("src")) === base + "/api/v1/preview?view=live",
    "the peer's live preview stream",
  );
  assert.equal(await peer.locator(".unit-preview-toggle").getAttribute("aria-pressed"), "true");
  assert.equal(
    await ui.localCard().locator(".unit-preview").getAttribute("src"),
    null,
    "starting one preview must not start another",
  );

  await peer.locator(".unit-preview-view").selectOption("delayed");
  await until(
    async () => (await peer.locator(".unit-preview").getAttribute("src")) === base + "/api/v1/preview?view=delayed",
    "the running stream to swap view",
  );

  await peer.locator(".unit-preview-toggle").click();
  assert.equal(await peer.locator(".unit-preview").getAttribute("src"), null, "the stream must be dropped, not hidden");
  assert.equal(await peer.locator(".unit-preview-toggle").getAttribute("aria-pressed"), "false");
});

// UI-7: clips download straight from the mirror that recorded them.
test("UI-7 a card's download asks its own mirror, with that card's format", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre", status: { buffer: { capacity_s: 30 } } }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 2, "two cards");
  const peer = ui.card("unit-b");
  const peerBtn = peer.locator(".unit-download");
  await until(async () => (await peerBtn.textContent()) === "Save last 30 s", "the peer's own buffer length");

  await peerBtn.click();
  const first = await ui.fleet.waitFor((c) => c.path === "/api/v1/clip" && c.unit === "unit-b", "a clip from unit-b");
  assert.equal(first.params.get("seconds"), "30", "the clip length follows that mirror's buffer");
  assert.equal(first.params.get("format"), "mp4");
  assert.equal(first.origin, ui.fleet.unit("unit-b").base, "the video must not take a second hop");

  // Format is per card too.
  await peer.locator(".unit-format").selectOption("mjpeg");
  await until(async () => await peerBtn.isEnabled(), "the download button to free up");
  await peerBtn.click();
  await ui.fleet.waitFor(
    (c) => c.path === "/api/v1/clip" && c.unit === "unit-b" && c.params.get("format") === "mjpeg",
    "an mjpeg clip from unit-b",
  );

  const localBtn = ui.localCard().locator(".unit-download");
  await localBtn.click();
  const own = await ui.fleet.waitFor((c) => c.path === "/api/v1/clip" && c.unit === "unit-a", "a clip from this mirror");
  assert.equal(own.params.get("format"), "mp4", "one card's format select must not change another's");
  assert.equal(own.params.get("seconds"), "60");
});

// UI-8: it has to work on the phone in the dancer's hand.
test("UI-8 three cards fit a phone: no sideways scroll, thumb-sized controls", async (t) => {
  const ui = await openUI({
    viewport: { width: 390, height: 844 },
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre" }, { id: "unit-c", name: "Window" }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 3, "three cards");

  const overflow = await ui.page.evaluate(() => {
    const d = document.documentElement;
    return d.scrollWidth - d.clientWidth;
  });
  assert.ok(overflow <= 0, `page scrolls sideways by ${overflow}px on a 390px screen`);

  const bad = await ui.page.evaluate(() => {
    const out = [];
    document.querySelectorAll("#units .unit-card").forEach((card) => {
      card.querySelectorAll("button, select, input[type=range]").forEach((el) => {
        const r = el.getBoundingClientRect();
        if (r.height < 44) out.push(`${el.className || el.tagName} is ${Math.round(r.height)}px tall`);
        if (r.right > window.innerWidth + 0.5) out.push(`${el.className || el.tagName} runs off the screen`);
      });
    });
    return out;
  });
  assert.deepEqual(bad, [], "controls must stay thumb-sized and on-screen");

  // One column on a phone: every card starts at the same x.
  const lefts = await ui.page.evaluate(() =>
    [...document.querySelectorAll("#units .unit-card")].map((c) => Math.round(c.getBoundingClientRect().left)),
  );
  assert.equal(new Set(lefts).size, 1, "cards must stack in one column on a phone");
});

// UI-11: a card is as tall as its own contents. Side by side, a card whose
// preview is running must not stretch its neighbour into a wall of empty
// panel — which is exactly what a grid row does if you let it.
test("UI-11 opening one card's preview does not stretch the cards beside it", async (t) => {
  const ui = await openUI({
    viewport: { width: 1100, height: 900 },
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre" }, { id: "unit-c", name: "Window" }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 3, "three cards");
  const heightOf = (id) => ui.card(id).evaluate((el) => el.getBoundingClientRect().height);
  const before = await heightOf("unit-b"); // the card sharing this one's row

  await ui.localCard().locator(".unit-preview-toggle").click();
  await until(async () => (await ui.localCard().locator(".unit-preview").getAttribute("src")) !== null, "the stream");

  const after = await heightOf("unit-b");
  assert.equal(Math.round(after), Math.round(before), "a neighbour's card grew with someone else's preview");
});

// UI-9: each card's slider spans that mirror's own buffer.
test("UI-9 a card's slider range follows that mirror's buffer capacity", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre", status: { buffer: { capacity_s: 30 } } }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 2, "two cards");
  await until(async () => (await ui.card("unit-b").locator(".unit-delay").getAttribute("max")) === "30", "peer max");
  assert.equal(await ui.localCard().locator(".unit-delay").getAttribute("max"), "60");
});

// UI-10: a rejected setting says why, in the server's own words (FR-11).
test("UI-10 a rejected delay surfaces the problem+json detail", async (t) => {
  const ui = await openUI({ local: { unit_id: "unit-a", name: "Corner" } });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 1, "one card");
  ui.fleet.unit("unit-a").reject = { title: "invalid delay", detail: "delay must be between 0 and 60 s" };

  await setSlider(ui.localCard().locator(".unit-delay"), 5);
  await until(
    async () => (await ui.page.locator("#toast").textContent()).includes("delay must be between 0 and 60 s"),
    "the server's rejection message",
  );
  assert.equal(await ui.page.locator("#toast").isVisible(), true);
});
