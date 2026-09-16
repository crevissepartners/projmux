<script lang="ts">
  // Ctrl-K: jump to any live agent pane in any project by part of its name.
  // Rows waiting on you sort first, so an empty query already answers "what
  // needs me".
  import { activityOf, byAttention } from "../lib/activity";
  import { t } from "../lib/i18n.svelte";
  import { go } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { livePanes, paneLabel, slotRef, type Located } from "../lib/tree";
  import Picker from "./Picker.svelte";

  let { onClose }: { onClose: () => void } = $props();
  let query = $state("");
  let at = $state(0);
  let input: HTMLInputElement | undefined = $state();
  let list: HTMLElement | undefined = $state();
  $effect(() => input?.focus());

  const all = $derived(livePanes(live.tree).sort(byAttention));
  const rows = $derived.by(() => {
    const words = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
    return all.filter(({ project, win, pane }) => {
      const hay = `${paneLabel(pane).name} ${pane.agent?.provider} ${project.name} ${win.name} ${pane.runtimeId}`.toLowerCase();
      return words.every((w) => hay.includes(w));
    });
  });
  $effect(() => {
    if (at >= rows.length) at = Math.max(0, rows.length - 1);
  });
  $effect(() => {
    list?.children[at]?.scrollIntoView({ block: "nearest" });
  });

  function pick(row: Located) {
    onClose();
    go({ project: row.project.uid, window: row.win.uid, pane: slotRef(row.pane) });
  }

  function keydown(e: KeyboardEvent) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (rows.length) at = (at + (e.key === "ArrowDown" ? 1 : -1) + rows.length) % rows.length;
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (rows[at]) pick(rows[at]);
    } else if (e.key === "Escape") {
      e.preventDefault();
      onClose();
    }
  }
</script>

<Picker title={t("web.switcher.title")} hint="Ctrl-K" {onClose}>
  <input
    class="picker-query"
    placeholder={t("web.switcher.filter")}
    bind:this={input}
    bind:value={query}
    oninput={() => (at = 0)}
    onkeydown={keydown}
  />
  <div class="picker-list" bind:this={list}>
    {#each rows as row, i (row.pane.uid)}
      {@const state = activityOf(row.pane)}
      <button
        type="button"
        class="picker-row"
        data-provider={row.pane.agent?.provider}
        class:selected={i === at}
        onclick={() => pick(row)}
      >
        <span class="slot-dot"></span>
        <span class="name">{paneLabel(row.pane).name}</span>
        <span class="slot-activity {state?.tone || ''}">{state ? t(`web.activity.${state.kind}`) : ""}</span>
        <span class="where">{row.project.name} / {row.win.name}</span>
      </button>
    {:else}
      <div class="empty">{t("web.switcher.none")}</div>
    {/each}
  </div>
</Picker>
