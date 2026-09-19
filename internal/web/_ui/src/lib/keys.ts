// Alt chords, matched the same on every platform's keyboard.

type ChordEvent = Pick<KeyboardEvent, "altKey" | "ctrlKey" | "metaKey" | "shiftKey" | "key" | "code" | "getModifierState">;

const plain = /^[a-z0-9]$/i;

/**
 * Whether `event` is Alt plus the digit or letter `char`. Linux and Windows
 * leave the character alone under Alt, so `event.key` answers. macOS composes
 * under Option (Option-1 is "¡", Option-N a dead key), so there the physical
 * key answers instead. AltGr is never a chord: it has a character to type, and
 * Windows reports it as Ctrl+Alt.
 */
export function altChord(event: ChordEvent, char: string): boolean {
  if (!event.altKey) return false;
  if (event.key.toLowerCase() === char) return true;
  // A letter or digit is what the layout meant (Dvorak, say), not a composition.
  if (plain.test(event.key)) return false;
  if (event.ctrlKey || event.metaKey || event.shiftKey || event.getModifierState("AltGraph")) return false;
  return event.code === (/\d/.test(char) ? `Digit${char}` : `Key${char.toUpperCase()}`);
}
