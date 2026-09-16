import { explain } from "./errors";

// A refusal is a toast, not a status-bar corner: most actions here can be
// refused, and a reason written into the bottom row is one nobody reads.

export interface Toast {
  id: number;
  text: string;
  detail: string;
  tone: "" | "err";
}

export const toasts = $state<Toast[]>([]);
let next = 1;

export function toast(text: string, tone: "" | "err" = "", detail = ""): void {
  // The same message twice in a row is one toast, not a stack of them.
  const last = toasts.at(-1);
  if (last && last.text === text) return;
  const id = next++;
  toasts.push({ id, text, detail, tone });
  setTimeout(() => dismiss(id), tone === "err" ? 9000 : 3000);
}

export function dismiss(id: number): void {
  const at = toasts.findIndex((t) => t.id === id);
  if (at >= 0) toasts.splice(at, 1);
}

export function fail(err: unknown): void {
  const { text, detail } = explain(err);
  toast(text, "err", detail);
}
