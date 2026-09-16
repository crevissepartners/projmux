<script lang="ts">
  // The two-row bar projmux draws at the bottom of its tmux client
  // (docs/statusbar.md): row 0 the notify HUD and the usage HUD, row 1 the
  // session, path, host load and clock.
  import { t } from "../lib/i18n.svelte";
  import { live } from "../lib/state.svelte";
  import Meter from "./Meter.svelte";

  interface Props {
    session: string;
    path: string;
    onNotify: () => void;
  }
  let { session, path, onNotify }: Props = $props();

  const total = $derived(live.notifications.length);
  const critical = $derived(live.notifications.filter((n) => n.severity === "critical").length);
  const counts = $derived.by(() => {
    let windows = 0;
    let panes = 0;
    let agents = 0;
    for (const p of live.tree.projects) {
      windows += p.windows.length;
      for (const w of p.windows) {
        panes += w.panes.length;
        agents += w.agentCount;
      }
    }
    return `project ${live.tree.projects.length} · window ${windows} · pane ${panes} · agent ${agents}`;
  });

  let clock = $state("");
  $effect(() => {
    const tick = () => {
      const now = new Date();
      clock = `${String(now.getHours()).padStart(2, "0")}:${String(now.getMinutes()).padStart(2, "0")}`;
    };
    tick();
    const timer = setInterval(tick, 15_000);
    return () => clearInterval(timer);
  });
</script>

<footer class="statusbar">
  <div class="srow r0">
    <button type="button" class="seg notify" title={t("web.statusbar.notify_title")} onclick={onNotify}>
      <span class="n" class:crit={critical > 0} class:zero={!total}>
        {critical ? `notify ${total} · crit ${critical}` : `notify ${total}`}
      </span>
    </button>
    <span class="grow"></span>
    <span class="seg usage">
      {#each live.usage.hud as cell, i (i)}
        <Meter
          label="{cell.model} {cell.window}"
          pct={cell.pct}
          stale={cell.stale}
          title={cell.stale ? t("web.statusbar.stale") : ""}
        />
      {/each}
    </span>
  </div>
  <div class="srow r1">
    <span class="seg session">{session ? `[${session}]` : ""}</span>
    <span class="seg path" title={path}>{path}</span>
    <span class="grow"></span>
    <span class="seg tree" class:crit={!live.connected || !!live.error}>
      {!live.connected ? t("web.status.disconnected") : live.error || counts}
    </span>
    <span class="seg sys">
      {#if live.system.cpuPercent !== null}<Meter label="CPU" pct={live.system.cpuPercent} />{/if}
      {#if live.system.memoryPercent !== null}<Meter label="MEM" pct={live.system.memoryPercent} />{/if}
    </span>
    <span class="seg clock">{clock}</span>
  </div>
</footer>
