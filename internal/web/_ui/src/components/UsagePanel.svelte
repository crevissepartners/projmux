<script lang="ts">
  // What the terminal's usage popup lists, from the same cache read: every
  // window with its percentage, reset, and age, plus providers usage cannot
  // be read for.
  import { t } from "../lib/i18n.svelte";
  import { ago, fullTime } from "../lib/time";
  import type { Usage, UsageCell } from "../lib/types";

  let { usage }: { usage: Usage } = $props();

  // The cache is fresh for a minute after a collection, as the popup marks it.
  const syncStale = $derived(!!usage.lastSync && Date.now() - new Date(usage.lastSync).getTime() > 60_000);
  const counts = $derived(usage.rows.some((row) => row.limit));

  function span(seconds: number): string {
    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return t("web.usage.span_m", { m: minutes });
    const hours = Math.floor(minutes / 60);
    if (hours < 48) return t("web.usage.span_h", { h: hours, m: minutes % 60 });
    return t("web.usage.span_d", { d: Math.floor(hours / 24), h: hours % 24 });
  }

  function reset(row: UsageCell): string {
    if (row.resetsAt) {
      const left = (new Date(row.resetsAt).getTime() - Date.now()) / 1000;
      return left > 0 ? t("web.usage.reset_in", { span: span(left) }) : fullTime(row.resetsAt);
    }
    if (row.resetInSeconds !== undefined) return t("web.usage.reset_in", { span: span(row.resetInSeconds) });
    return "-";
  }

  const level = (pct: number) => (pct >= 90 ? "full" : pct >= 70 ? "hot" : "");
  const number = (n?: number) => (n === undefined ? "-" : n.toLocaleString());
</script>

<p class="popover-sub">{t("web.usage.subtitle")}</p>
<p class="usage-sync" class:dim={syncStale}>
  <span class="k">{t("web.usage.sync")}</span>
  {#if usage.lastSync}
    <span title={fullTime(usage.lastSync)}>{fullTime(usage.lastSync)} · {ago(usage.lastSync)}</span>
    {#if usage.syncSource === "cache mtime"}<span class="tflag soft">{t("web.usage.from_cache")}</span>{/if}
  {:else}
    <span>{t("web.usage.never")}</span>
  {/if}
</p>
{#if usage.error}
  <p class="notice err">{t("web.usage.unavailable", { reason: usage.error })}</p>
{:else if !usage.rows.length && !usage.unsupported?.length}
  <p class="notice">{t("web.usage.none")}</p>
{:else}
  <table class="usage-table">
    <thead>
      <tr>
        <th>{t("web.usage.col_model")}</th>
        <th>{t("web.usage.col_window")}</th>
        {#if counts}<th class="num">{t("web.usage.col_used")}</th><th class="num">{t("web.usage.col_limit")}</th>{/if}
        <th class="num">%</th>
        <th>{t("web.usage.col_reset")}</th>
        <th>{t("web.usage.col_age")}</th>
      </tr>
    </thead>
    <tbody>
      {#each usage.rows as row, i (i)}
        <tr class:dim={row.stale}>
          <td>
            {row.model}
            {#if row.fallback}<span class="tflag soft">{t("web.usage.fallback")}</span>{/if}
            {#if row.stale}<span class="tflag soft">{t("web.usage.stale")}</span>{/if}
          </td>
          <td class="window">{row.window}</td>
          {#if counts}<td class="num">{number(row.used)}</td><td class="num">{number(row.limit)}</td>{/if}
          <td class="num pct {level(row.pct)}">{row.pct}%</td>
          <td title={fullTime(row.resetsAt)}>{reset(row)}</td>
          <td>{row.updatedAt ? ago(row.updatedAt) : "-"}</td>
        </tr>
      {/each}
      {#each usage.unsupported || [] as provider (provider.model)}
        <tr class="dim">
          <td>{provider.label || provider.model}</td>
          <td class="window">-</td>
          {#if counts}<td class="num">-</td><td class="num">-</td>{/if}
          <td class="num">-</td>
          <td colspan="2">{t("web.usage.unsupported")}</td>
        </tr>
      {/each}
    </tbody>
  </table>
{/if}
