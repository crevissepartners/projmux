<script lang="ts">
  import { t } from "../lib/i18n.svelte";
  import type { WindowView } from "../lib/tree";

  let { win }: { win: WindowView } = $props();
</script>

<h3>{win.name}</h3>
<dl class="kv">
  <dt>uid</dt>
  <dd>{win.uid}</dd>
  <dt>runtime</dt>
  <dd>{win.runtimeId || t("web.slot.none")}</dd>
  <dt>panes</dt>
  <dd>{win.panes.length}</dd>
  <dt>agents</dt>
  <dd>{win.agentCount}</dd>
  {#if win.detached.length}
    <dt>detached agents</dt>
    <dd>{win.detached.length}</dd>
  {/if}
</dl>
{#if win.unbound}
  <h4>{t("web.window.warning")}</h4>
  <p class="empty" style:padding="0">{win.unboundReason || t("web.window.unbound")}</p>
{/if}
