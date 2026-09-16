// UI text comes from projmux's own message catalog, so the web client speaks
// the locale the CLI does and adds no second translation store. The server
// sends every `web.*` key for the resolved locale; placeholders are `{name}`.

import { get } from "./api";

let messages = $state<Record<string, string>>({});
let locale = $state("en-US");

export async function loadMessages(): Promise<void> {
  try {
    const body = await get<{ locale: string; messages: Record<string, string> }>("/api/v1/web/i18n");
    messages = body.messages;
    locale = body.locale;
    document.documentElement.lang = body.locale;
  } catch {
    /* keys render as themselves, which is ugly but readable */
  }
}

export function currentLocale(): string {
  return locale;
}

export function t(key: string, vars: Record<string, string | number> = {}): string {
  const template = messages[key] ?? key;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) => (name in vars ? String(vars[name]) : whole));
}
