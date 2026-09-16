import { t } from "./i18n.svelte";

/** Clock time for today, date and time otherwise. */
export function shortTime(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const hm = date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false });
  if (date.toDateString() === new Date().toDateString()) return hm;
  return `${date.getMonth() + 1}/${date.getDate()} ${hm}`;
}

export function fullTime(value?: string): string {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function ago(value?: string): string {
  if (!value) return "";
  const then = new Date(value).getTime();
  if (Number.isNaN(then)) return "";
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000));
  if (seconds < 60) return t("web.time.seconds_ago", { n: seconds });
  if (seconds < 3600) return t("web.time.minutes_ago", { n: Math.floor(seconds / 60) });
  if (seconds < 86_400) return t("web.time.hours_ago", { n: Math.floor(seconds / 3600) });
  return t("web.time.days_ago", { n: Math.floor(seconds / 86_400) });
}
