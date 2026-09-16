// Messages this page sent that the transcript does not show yet.
//
// A message to a busy Claude session is written to its transcript only when
// the session picks it up at its next tool boundary, so it can arrive
// seconds or minutes after the send. Until then the conversation shows it as
// pending, and the line goes away when the transcript records it.

export interface Pending {
  id: number;
  agent: string;
  text: string;
  /** The broker's message ref, which the recorded turn carries. */
  messageRef: string;
  sentAt: number;
}

/** After this long a pending line says it was sent but is not recorded yet. */
export const PENDING_LATE_MS = 30_000;
/** After this long it is dropped; the send receipt already said what happened. */
const PENDING_DROP_MS = 10 * 60_000;

export const pending = $state<Pending[]>([]);
let next = 1;

export function addPending(agent: string, text: string, messageRef = ""): void {
  pending.push({ id: next++, agent, text, messageRef, sentAt: Date.now() });
}

/** Drop what a recorded turn answers: the same ref, or the same text. */
export function settle(agent: string, turn: { role: string; text: string; messageRef?: string }): void {
  const at = pending.findIndex(
    (p) =>
      p.agent === agent &&
      ((p.messageRef && p.messageRef === turn.messageRef) ||
        (turn.role === "user" && p.text.trim() === turn.text.trim())),
  );
  if (at >= 0) pending.splice(at, 1);
}

setInterval(() => {
  const cutoff = Date.now() - PENDING_DROP_MS;
  for (let i = pending.length - 1; i >= 0; i--) {
    if (pending[i].sentAt < cutoff) pending.splice(i, 1);
  }
}, 30_000);
