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

// UI-12: the line explaining the cards has to read as an intro to the group,
// not as a label stuck to the first card.
test("UI-12 the fleet hint clears the first card", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [{ id: "unit-b", name: "Barre" }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 2, "two cards");

  const gap = await ui.page.evaluate(() => {
    const hint = document.getElementById("fleet-hint").getBoundingClientRect();
    const card = document.querySelector("#units .unit-card").getBoundingClientRect();
    return Math.round(card.top - hint.bottom);
  });
  assert.ok(gap >= 12, `only ${gap}px between the hint and the first card`);
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

// UI-13: a warming mirror shows a still frame — that is FR-10 working — but
// the field report was "I changed the delay, nothing happened, the picture
// froze, it looked like a crash". The card says how long the wait is, so a
// frozen picture reads as a countdown.
test("UI-13 a warming card counts down to when the delay is reachable", async (t) => {
  const ui = await openUI({
    local: {
      unit_id: "unit-a",
      name: "Corner",
      delay_s: 25,
      warming_up: true,
      buffer: { capacity_s: 60, filled_s: 13, bytes: 1 },
    },
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 1, "one card");
  const badge = ui.localCard().locator(".unit-badge");
  await until(async () => (await badge.textContent()) === "Ready in 12 s", "the countdown");
  assert.equal(await badge.isVisible(), true);

  // The buffer fills; the wait shrinks with it.
  ui.fleet.unit("unit-a").status.buffer.filled_s = 21.4;
  await until(async () => (await badge.textContent()) === "Ready in 4 s", "the shorter countdown");

  // Once the buffer reaches back far enough the badge goes away entirely.
  const st = ui.fleet.unit("unit-a").status;
  st.buffer.filled_s = 60;
  st.warming_up = false;
  await until(async () => (await badge.isHidden()), "the badge cleared");
});

// UI-13: raising the delay past what is buffered is allowed — the mirror
// warms into it — but the slider says which part of its range is not there
// yet, so nobody has to discover it by watching a still picture.
test("UI-13 the slider marks the range the buffer cannot serve yet", async (t) => {
  const ui = await openUI({
    local: {
      unit_id: "unit-a",
      name: "Corner",
      delay_s: 5,
      buffer: { capacity_s: 60, filled_s: 15, bytes: 1 },
    },
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 1, "one card");
  const slider = ui.localCard().locator(".unit-delay");
  const buffered = () => slider.evaluate((el) => el.style.getPropertyValue("--buffered"));

  await until(async () => (await buffered()) === "25%", "15 s of a 60 s range");

  ui.fleet.unit("unit-a").status.buffer.filled_s = 60;
  await until(async () => (await buffered()) === "100%", "a full buffer");
});

// UI-13: a mirror that stopped answering is Offline first and foremost — a
// countdown on a card nobody can reach would be a guess presented as fact.
test("UI-13 offline beats the countdown on the same badge", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner" },
    peers: [
      {
        id: "unit-b",
        name: "Barre",
        status: { delay_s: 30, warming_up: true, buffer: { capacity_s: 60, filled_s: 0, bytes: 0 } },
      },
    ],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 2, "two cards");
  const badge = ui.card("unit-b").locator(".unit-badge");
  await until(async () => (await badge.textContent()) === "Ready in 30 s", "the countdown");

  ui.fleet.unit("unit-b").down = true;
  await until(async () => (await badge.textContent()) === "Offline", "the offline marker");
});

// UI-14: the settings page is a second control plane onto the same unit, and
// the mirror flip is one shared setting, not a per-phone one. Read once at
// load, a page open on the wall tablet shows a stale toggle for as long as it
// stays open — and, worse, computes its next click from that stale value, so
// the click that should turn the flip off turns it on again.
test("UI-14 the mirror toggle follows a change made from another phone", async (t) => {
  const ui = await openUI({ path: "/settings.html", local: { config: { mirror_flip: false } } });
  t.after(() => ui.close());

  const toggle = ui.page.locator("#mirror-toggle");
  await until(async () => (await toggle.textContent()) === "Off", "the toggle to load");

  // Somebody else, on their own phone, turns the flip on.
  ui.fleet.local.config.mirror_flip = true;
  await until(async () => (await toggle.textContent()) === "On", "the toggle to follow the unit");
  assert.equal(await toggle.getAttribute("aria-pressed"), "true");

  // And this page's next click is computed from the unit's value rather than
  // the one it loaded with: it turns the flip off, instead of asking for the
  // "on" it is already at.
  await toggle.click();
  const patch = await ui.fleet.waitFor(
    (c) => c.method === "PATCH" && c.path === "/api/v1/config",
    "the mirror-flip PATCH",
  );
  assert.deepEqual(JSON.parse(patch.body), { mirror_flip: false });

  // The setting rides on the status poll, so watching a unit is one request
  // per second and not two (REQUIREMENTS §3).
  assert.equal(
    ui.fleet.calls.filter((c) => c.method === "GET" && c.path === "/api/v1/config").length,
    0,
    "the page polled /config as well as /status",
  );
});

// UI-14: polling a setting you can also write is a lost-update race. A read
// made before the click can be delivered after it, carrying the value from
// before — and applying that bounces the toggle back under the finger for a
// whole poll interval. It is the reading UI-4 keeps off the slider, on a
// control with no drag to recognise it by.
test("UI-14 a read made before the click cannot undo it when it lands after", async (t) => {
  const ui = await openUI({ path: "/settings.html", local: { config: { mirror_flip: false } } });
  t.after(() => ui.close());

  const toggle = ui.page.locator("#mirror-toggle");
  await until(async () => (await toggle.textContent()) === "Off", "the toggle to load");

  // Every read now takes 2.5 s, so reads taken before the click keep
  // arriving — reporting "off" — for 2.5 s after it.
  ui.fleet.local.slowRead = 2500;
  await sleep(1200); // at least one such read is in flight
  await toggle.click();
  await until(async () => (await toggle.textContent()) === "On", "the toggle to show the click");

  await sleep(3000);
  assert.equal(await toggle.textContent(), "On", "a stale read undid the click");
  assert.equal(await toggle.getAttribute("aria-pressed"), "true");
});

// UI-15: the mirror flip is per mirror, on the mirror's own card (FR-2, FR-14).
//
// It used to live only on the settings page, which is a page about *this*
// unit: a guest standing in front of one mirror, with the host's control page
// open on their phone, flipped a different unit's TV and read the one in front
// of them as broken. Every other control in this page obeys the rule — a card
// only ever changes its own mirror — and this is the one that did not.
test("UI-15 each card's mirror flip patches its own mirror and no other", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner", config: { mirror_flip: false } },
    peers: [
      { id: "unit-b", name: "Barre", config: { mirror_flip: false } },
      { id: "unit-c", name: "Window", config: { mirror_flip: false } },
    ],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 3, "three cards");
  for (const id of ["unit-a", "unit-b", "unit-c"]) {
    assert.equal(await ui.card(id).locator(".unit-mirror").count(), 1, `${id} has no mirror flip`);
  }

  const barre = ui.card("unit-b").locator(".unit-mirror");
  await until(async () => (await barre.textContent()) === "Off", "the toggle to load Barre's value");
  await barre.click();

  const patch = await ui.fleet.waitFor(
    (c) => c.method === "PATCH" && c.path === "/api/v1/config" && c.unit === "unit-b",
    "a config PATCH to unit-b",
  );
  assert.deepEqual(JSON.parse(patch.body), { mirror_flip: true });
  assert.equal(patch.origin, ui.fleet.unit("unit-b").base, "the mirror is addressed directly, not proxied");

  const strays = ui.fleet.calls.filter((c) => c.method === "PATCH" && c.unit !== "unit-b");
  assert.deepEqual(strays, [], "flipping one mirror must not touch another");

  // The card shows what that unit came back with, and the other cards are
  // left exactly as they were.
  await until(async () => (await barre.textContent()) === "On", "Barre's toggle to show the flip");
  assert.equal(await ui.card("unit-a").locator(".unit-mirror").textContent(), "Off");
  assert.equal(await ui.card("unit-c").locator(".unit-mirror").textContent(), "Off");

  // And this mirror's own card posts to the page's origin.
  await ui.localCard().locator(".unit-mirror").click();
  const own = await ui.fleet.waitFor(
    (c) => c.method === "PATCH" && c.path === "/api/v1/config" && c.unit === "unit-a",
    "a config PATCH to this mirror",
  );
  assert.equal(own.origin, ui.origin);
});

// UI-15: the flip is the unit's state, not the card's. Two phones and the
// settings page all look at the same setting, so a card follows a change made
// somewhere else — and computes its next click from the unit's value, not from
// the one the page loaded with (the reasoning is UI-14's, per card).
test("UI-15 a card's mirror flip follows that mirror and survives a stale read", async (t) => {
  const ui = await openUI({
    local: { unit_id: "unit-a", name: "Corner", config: { mirror_flip: false } },
    peers: [{ id: "unit-b", name: "Barre", config: { mirror_flip: false } }],
  });
  t.after(() => ui.close());

  await until(async () => (await ui.cards().count()) === 2, "two cards");
  const barre = ui.card("unit-b").locator(".unit-mirror");
  await until(async () => (await barre.textContent()) === "Off", "the toggle to load");

  // Somebody at the other mirror turns its flip on.
  ui.fleet.unit("unit-b").config.mirror_flip = true;
  await until(async () => (await barre.textContent()) === "On", "the card to follow the mirror");
  assert.equal(await barre.getAttribute("aria-pressed"), "true");

  // A read taken before the next click must not undo it when it lands after.
  ui.fleet.unit("unit-b").slowRead = 2500;
  await sleep(1200);
  await barre.click();
  await until(async () => (await barre.textContent()) === "Off", "the toggle to show the click");
  await sleep(3000);
  assert.equal(await barre.textContent(), "Off", "a stale read undid the click");

  // One card's stale-read guard is its own: this mirror's card keeps polling
  // and stays right.
  assert.equal(await ui.localCard().locator(".unit-mirror").textContent(), "Off");
});

// UI-15: the settings page has to say which mirror it is about. It only ever
// controls the unit that served it, and a page that names no unit is one a
// guest can act on by mistake with three mirrors in the room.
test("UI-15 the settings page names the mirror it is about", async (t) => {
  const ui = await openUI({ path: "/settings.html", local: { name: "Long Side" } });
  t.after(() => ui.close());

  await until(async () => (await ui.page.locator("#st-name").textContent()) === "Long Side", "the unit's name");
  const hint = await ui.page.locator("main .hint").first().textContent();
  assert.match(hint, /this\s+mirror/i, "the page does not say the flip is this unit's");
});
