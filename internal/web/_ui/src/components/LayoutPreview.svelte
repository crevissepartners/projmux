<script lang="ts">
  // The window as tmux draws it: every pane where tmux put it, with what is
  // on it. Geometry is in cells, so the preview is a scaled copy of the real
  // layout. Off by default: it captures every pane on a timer.
  import { onDestroy } from "svelte";
  import { paths } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { go } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { paneByRuntime, slotRef } from "../lib/tree";
  import type { Layout, Run } from "../lib/types";

  interface Props {
    window: string;
    ownRuntime: string;
    onClose: () => void;
  }
  let { window, ownRuntime, onClose }: Props = $props();

  let layout = $state<Layout | null>(null);
  let status = $state<"live" | "warn" | "gone">("live");
  let error = $state("");
  let source: EventSource | null = null;

  $effect(() => {
    source?.close();
    layout = null;
    error = "";
    const stream = new EventSource(`${paths.layout(window)}/events?contents=1`);
    source = stream;
    stream.addEventListener("layout", (event) => {
      try {
        layout = JSON.parse((event as MessageEvent).data) as Layout;
        status = "live";
      } catch {
        /* skip one malformed frame */
      }
    });
    stream.addEventListener("gone", () => {
      status = "gone";
      stream.close();
    });
    stream.addEventListener("error", (event) => {
      // Only the EventSource's own error is a lost connection.
      if (event instanceof MessageEvent) return;
      if (status !== "gone") status = "warn";
      if (!layout) error = t("web.status.reconnecting");
    });
    return () => stream.close();
  });
  onDestroy(() => source?.close());

  // The server has parsed the escapes; this only styles text. Reverse with no
  // explicit pair swaps the terminal's own default colours.
  function runStyle(run: Run): string {
    const fg = run.r ? run.b || "var(--well)" : run.f;
    const bg = run.r ? run.f || "var(--ink)" : run.b;
    const parts: string[] = [];
    if (fg) parts.push(`color:${fg}`);
    if (bg) parts.push(`background:${bg}`);
    if (run.bo) parts.push("font-weight:600");
    if (run.d) parts.push("opacity:0.62");
    if (run.i) parts.push("font-style:italic");
    if (run.u) parts.push("text-decoration:underline");
    return parts.join(";");
  }
</script>

<div class="layout-pop">
  <button type="button" class="tbtn" title={t("web.layout.close")} onclick={onClose}>×</button>
  <div class="screen-head">
    <span class="screen-title">
      {layout ? `${layout.window} · ${layout.panes.length} pane · ${layout.width}×${layout.height}` : ""}
    </span>
    <span class="tag {status}">{status === "live" ? "live" : status === "gone" ? t("web.status.gone") : t("web.status.reconnecting")}</span>
  </div>
  <div class="layout">
    {#if !layout}
      <div class="layout-grid">{error || t("web.layout.reading")}</div>
    {:else}
      <div class="layout-grid" style="--cols:{layout.width || 1};--rows:{layout.height || 1}">
        {#each layout.panes as pane (pane.runtime)}
          {@const target = paneByRuntime(live.tree, pane.runtime)}
          <div
            class="layout-pane"
            class:own={pane.runtime === ownRuntime}
            class:active={pane.active}
            class:clickable={!!target}
            style="--x:{pane.x};--y:{pane.y};--w:{pane.width};--h:{pane.height}"
            title="{pane.runtime} · {pane.command || '?'}"
            role="button"
            tabindex="-1"
            onclick={() => target && go({ project: target.project.uid, window: target.win.uid, pane: slotRef(target.pane) })}
            onkeydown={() => {}}
          >
            <div class="layout-screen">
              {#each pane.lines || [] as line, li (li)}
                <div class="screen-line">
                  {#each line as run, ri (ri)}<span style={runStyle(run)}>{run.t}</span>{:else}{" "}{/each}
                </div>
              {/each}
            </div>
            <span class="layout-tag">{`${pane.runtime} ${pane.command || ""}`.trim()}</span>
          </div>
        {/each}
      </div>
    {/if}
  </div>
</div>
