<script lang="ts">
  // Moves the operator's attached tmux client to this pane, where a prompt
  // the web cannot answer can be answered. It sends no keys.
  import { focusPane } from "../lib/commands";
  import { t } from "../lib/i18n.svelte";
  import { live } from "../lib/state.svelte";
  import { locatePane } from "../lib/tree";

  let { paneUID, compact = false }: { paneUID: string; compact?: boolean } = $props();
  const where = $derived(locatePane(live.tree, paneUID));
</script>

{#if where}
  <button
    type="button"
    class={compact ? "slot-btn jump" : "send jump"}
    title={t("web.terminal.open_title")}
    onmousedown={(e) => e.stopPropagation()}
    onclick={(e) => {
      e.stopPropagation();
      if (where) focusPane(where.project.uid, where.win.uid, where.pane.uid);
    }}>{t("web.terminal.open")}</button
  >
{/if}
