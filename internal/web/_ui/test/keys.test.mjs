import assert from "node:assert/strict";
import { test } from "node:test";

import { altChord } from "../src/lib/keys.ts";

function press({ key, code, alt = true, ctrl = false, meta = false, shift = false, altGraph = false }) {
  return {
    key,
    code,
    altKey: alt,
    ctrlKey: ctrl,
    metaKey: meta,
    shiftKey: shift,
    getModifierState: (name) => name === "AltGraph" && altGraph,
  };
}

test("Linux and Windows: Alt leaves the character alone", () => {
  assert.equal(altChord(press({ key: "1", code: "Digit1" }), "1"), true);
  assert.equal(altChord(press({ key: "n", code: "KeyN" }), "n"), true);
  assert.equal(altChord(press({ key: "N", code: "KeyN", shift: true }), "n"), true);
  assert.equal(altChord(press({ key: "1", code: "Numpad1" }), "1"), true);
});

test("macOS: Option composes a symbol, so the physical key decides", () => {
  const composed = { 1: "¡", 2: "™", 4: "¢", 5: "∞", 7: "¶" };
  for (const [digit, key] of Object.entries(composed)) {
    assert.equal(altChord(press({ key, code: `Digit${digit}` }), digit), true, `Option-${digit}`);
  }
  assert.equal(altChord(press({ key: "Dead", code: "KeyN" }), "n"), true, "Option-N is a dead key");
});

test("a chord is only its own key", () => {
  assert.equal(altChord(press({ key: "¡", code: "Digit1" }), "2"), false);
  assert.equal(altChord(press({ key: "2", code: "Digit2" }), "1"), false);
  assert.equal(altChord(press({ key: "1", code: "Digit1", alt: false }), "1"), false);
});

test("AltGr types its character instead of running a chord", () => {
  // German layout, AltGr-7 types "{". Windows reports AltGr as Ctrl+Alt.
  assert.equal(altChord(press({ key: "{", code: "Digit7", ctrl: true, altGraph: true }), "7"), false);
  assert.equal(altChord(press({ key: "{", code: "Digit7", ctrl: true }), "7"), false);
  assert.equal(altChord(press({ key: "{", code: "Digit7", altGraph: true }), "7"), false);
});

test("the physical key never overrides a letter or digit the layout produced", () => {
  // Dvorak: the key at the QWERTY N position types "b".
  assert.equal(altChord(press({ key: "b", code: "KeyN" }), "n"), false);
  assert.equal(altChord(press({ key: "n", code: "KeyL" }), "n"), true);
});

test("the physical key is not read with Shift or Meta held", () => {
  assert.equal(altChord(press({ key: "!", code: "Digit1", shift: true }), "1"), false);
  assert.equal(altChord(press({ key: "¡", code: "Digit1", meta: true }), "1"), false);
});

test("AZERTY: the unshifted digit row still reaches the digit chords", () => {
  assert.equal(altChord(press({ key: "&", code: "Digit1" }), "1"), true);
  assert.equal(altChord(press({ key: "1", code: "Digit1", shift: true }), "1"), true);
});
