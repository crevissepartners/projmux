// What a person reads when something is refused.
//
// The server's code is a stable token; the message is projmux's own wording,
// which names internal concepts. The page shows a sentence from the catalog
// for the code and keeps projmux's text folded under it, so the reason is one
// click away without being the first thing anyone has to parse.

import { ApiError } from "./api";
import { t } from "./i18n.svelte";

export interface Explained {
  text: string;
  detail: string;
}

// CLI refusals that only carry text today. A pattern here earns a sentence
// until projmux gives the refusal a token of its own.
const patterns: [RegExp, string][] = [
  [/no current Running Agent activation/i, "web.error.agent_not_ready"],
  [/registration lease is unavailable|not eligible/i, "web.error.agent_not_ready"],
  [/native (Codex )?control unavailable/i, "web.error.unavailable"],
  [/message not delivered/i, "web.error.not_delivered"],
  [/invalid-content|frame .*too large|too large/i, "web.error.message_too_large"],
];

export function explain(err: unknown): Explained {
  const detail = err instanceof Error ? err.message : String(err);
  if (err instanceof ApiError) {
    for (const [pattern, key] of patterns) {
      if (pattern.test(detail)) return { text: t(key), detail };
    }
    const key = `web.error.${err.code}`;
    const text = t(key);
    if (text !== key) return { text, detail };
  }
  return { text: t("web.error.generic"), detail };
}

/** The sentence for a message delivery state, or the state itself. */
export function deliveryText(state: string): string {
  const key = `web.delivery.${state}`;
  const text = t(key);
  return text === key ? state : text;
}

export function phaseText(phase: string): string {
  const key = `web.phase.${phase}`;
  const text = t(key);
  return text === key ? phase : text;
}

export function providerText(provider: string): string {
  const key = `web.provider.${provider}`;
  const text = t(key);
  return text === key ? provider : text;
}
