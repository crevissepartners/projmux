<script lang="ts">
  // The two-row bar projmux draws at the bottom of its tmux client
  // (docs/statusbar.md): row 0 the notify HUD and the usage HUD, row 1 the
  // Project, path, git, host load and clock. Which parts show is the
  // operator's Settings choice, read from the server; each part opens what
  // clicking it opens in the terminal.
  import { get, paths } from "../lib/api";
  import { t } from "../lib/i18n.svelte";
  import { live } from "../lib/state.svelte";
  import type { PaneGit, StatusbarParts, Usage } from "../lib/types";
  import CopyButton from "./CopyButton.svelte";
  import Meter from "./Meter.svelte";
  import Popover from "./Popover.svelte";
  import UsagePanel from "./UsagePanel.svelte";
  import { ui } from "../lib/ui.svelte";

  interface Props {
    session: string;
    path: string;
    paneUID: string;
    onNotify: () => void;
    onSession: () => void;
  }
  let { session, path, paneUID, onNotify, onSession }: Props = $props();

  // Every part shows until the server says otherwise, as the TUI defaults.
  let parts = $state<StatusbarParts>({
    notifications: true,
    usage: true,
    project: true,
    workingDirectory: true,
    git: true,
    resources: true,
    clock: true,
  });
  let git = $state<PaneGit | null>(null);

  $effect(() => {
    // A settings save reruns this; the usage rows it selects are read again.
    if (ui.settingsVersion) get<Usage>(paths.usage).then((body) => (live.usage = body), () => {});
    let last = "";
    const load = async () => {
      try {
        const next = await get<StatusbarParts>(paths.statusbar);
        const key = JSON.stringify(next);
        if (key === last) return;
        const changed = last !== "";
        last = key;
        parts = next;
        // The HUD rows are selected by the same Settings; a change there
        // does not touch the usage cache, so the stream would not resend.
        if (changed) live.usage = await get<Usage>(paths.usage);
      } catch {
        /* keep what is shown */
      }
    };
    load();
    const timer = setInterval(load, 30_000);
    return () => clearInterval(timer);
  });

  $effect(() => {
    const uid = paneUID;
    const wanted = parts.git;
    git = null;
    if (!uid || !wanted) return;
    let stopped = false;
    const load = async () => {
      try {
        const next = await get<PaneGit>(paths.paneGit(uid));
        if (!stopped) git = next;
      } catch {
        if (!stopped) git = null;
      }
    };
    load();
    const timer = setInterval(load, 15_000);
    return () => {
      stopped = true;
      clearInterval(timer);
    };
  });

  const gitMarks = $derived(
    git
      ? [
          git.dirty ? "*" : "",
          git.staged ? `+${git.staged}` : "",
          git.ahead ? `↑${git.ahead}` : "",
          git.behind ? `↓${git.behind}` : "",
        ].filter(Boolean)
      : [],
  );
  // As the terminal popup: "branch in repo" when the directory is not named
  // after the repository.
  const gitLine = $derived.by(() => {
    if (!git?.branch) return "";
    const base = (git.cwd || "").split("/").filter(Boolean).pop() || "";
    return git.repo && git.repo !== base ? t("web.path.branch_in", { branch: git.branch, repo: git.repo }) : git.branch;
  });

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
    {#if parts.notifications}
      <button type="button" class="seg notify" title={t("web.statusbar.notify_title")} onclick={onNotify}>
        <span class="n" class:crit={critical > 0} class:zero={!total}>
          {critical ? `notify ${total} · crit ${critical}` : `notify ${total}`}
        </span>
      </button>
    {/if}
    <span class="grow"></span>
    {#if parts.usage && (live.usage.hud.length || live.usage.error)}
      {#snippet usageFace()}
        {#each live.usage.hud as cell, i (i)}
          <Meter
            label="{cell.model} {cell.window}"
            pct={cell.pct}
            stale={cell.stale}
            fallback={!!cell.fallback}
            title={cell.stale ? t("web.statusbar.stale") : ""}
          />
        {:else}
          <span class="n">{t("web.usage.unavailable_short")}</span>
        {/each}
      {/snippet}
      <Popover className="seg usage" faceSnippet={usageFace} title={t("web.usage.title")} side="above" wide>
        <UsagePanel usage={live.usage} />
      </Popover>
    {/if}
  </div>
  <div class="srow r1">
    {#if parts.project && session}
      <button type="button" class="seg session" title={t("web.statusbar.session_title")} onclick={onSession}>[{session}]</button>
    {/if}
    {#if parts.workingDirectory && path}
      <Popover className="seg path" face={path} title={t("web.path.title")} side="above" align="start">
        <p class="popover-sub">{t("web.path.subtitle")}</p>
        <dl class="kv">
          <dt>cwd</dt>
          <dd class="with-copy"><span>{path}</span><CopyButton text={path} /></dd>
          {#if session}<dt>project</dt><dd>{session}</dd>{/if}
          {#if gitLine}<dt>git</dt><dd>{gitLine}</dd>{/if}
        </dl>
      </Popover>
    {/if}
    {#if parts.git && git?.branch}
      <span class="seg git" title={gitLine}>
        <span class="branch">{git.branch}</span>
        {#each gitMarks as mark (mark)}<span class="mark">{mark}</span>{/each}
      </span>
    {/if}
    <span class="grow"></span>
    <span class="seg tree" class:crit={!live.connected || !!live.error}>
      {!live.connected ? t("web.status.disconnected") : live.error || counts}
    </span>
    {#if parts.resources}
      <span class="seg sys">
        {#if live.system.cpuPercent !== null}<Meter label="CPU" pct={live.system.cpuPercent} />{/if}
        {#if live.system.memoryPercent !== null}<Meter label="MEM" pct={live.system.memoryPercent} />{/if}
      </span>
    {/if}
    {#if parts.clock}<span class="seg clock">{clock}</span>{/if}
  </div>
</footer>
