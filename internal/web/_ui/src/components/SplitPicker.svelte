<script lang="ts">
  // Alt-7: open an existing pane beside this one, resume an Offline agent, or
  // split a real tmux Pane. The first is a view and free; the other two start
  // provider sessions that spend quota, so creating never runs on one click:
  // the exact command is fetched from the builder the server runs and shown
  // before the create button appears.
  import { get, paths, post } from "../lib/api";
  import { explain, phaseText, providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { go, route, setExtras } from "../lib/router.svelte";
  import { live, refresh } from "../lib/state.svelte";
  import { ago } from "../lib/time";
  import { fail } from "../lib/toast.svelte";
  import { locateSlot, paneLabel, slotRef, type Located, type PaneView } from "../lib/tree";
  import type { Agent, Pane, ResumeCandidate } from "../lib/types";
  import { ui } from "../lib/ui.svelte";
  import Picker from "./Picker.svelte";

  let { onClose }: { onClose: () => void } = $props();

  const sel = route.sel;
  let query = $state("");
  let input: HTMLInputElement | undefined = $state();
  $effect(() => input?.focus());

  const candidates = $derived.by(() => {
    const open = new Set([route.sel.pane, ...route.extras].filter(Boolean));
    const isOpen = (pane: PaneView) => open.has(pane.uid) || (!!pane.agent && open.has(pane.agent.uid));
    const rows: Located[] = [];
    for (const project of live.tree.projects) {
      for (const win of project.windows) {
        for (const pane of win.panes) {
          if (isOpen(pane) || !pane.runtimeId) continue;
          if (!pane.agent && !ui.showShell) continue;
          rows.push({ project, win, pane });
        }
      }
    }
    const needle = query.trim().toLowerCase();
    return rows.filter(
      ({ project, win, pane }) =>
        !needle ||
        `${paneLabel(pane).name} ${pane.agent?.provider || ""} ${project.name} ${win.name}`.toLowerCase().includes(needle),
    );
  });

  function beside(row: Located) {
    onClose();
    setExtras([...route.extras, slotRef(row.pane)]);
  }

  // Resume candidates: agents this window owns whose pane is gone. Each row
  // carries the first and last line of its conversation, because a name and a
  // provider do not tell two sessions in the same repository apart.
  let resumable = $state<ResumeCandidate[]>([]);
  if (sel.window) {
    get<{ items: ResumeCandidate[] }>(paths.resumeCandidates(sel.window))
      .then((body) => (resumable = body.items))
      .catch(() => {
        /* the picker still works without the resume half */
      });
  }

  let resuming = $state("");
  async function resume(candidate: ResumeCandidate) {
    resuming = candidate.uid;
    try {
      await post(`${paths.agent(candidate.uid)}/resume`, { confirm: true });
      await refresh();
      onClose();
      if (sel.project && sel.window) go({ project: sel.project, window: sel.window, pane: candidate.uid });
    } catch (err) {
      fail(err);
    } finally {
      resuming = "";
    }
  }

  // Everything the web creates is an Agent, Claude unless picked otherwise.
  const kinds = ["claude", "codex", "antigravity"];
  let kind = $state("claude");
  let payload = $state("");
  // A new Claude session can start on another model or effort. The choices
  // are the aliases and levels `claude --help` names; empty keeps the
  // operator's own default.
  const claudeModels = ["", "opus", "sonnet", "fable"];
  const claudeEfforts = ["", "low", "medium", "high", "xhigh", "max"];
  let model = $state("");
  let effort = $state("");
  let preview = $state("");
  let previewError = $state("");
  let creating = $state(false);

  // The selection names a slot by agent or pane; the split anchors on the pane.
  const anchor = $derived(sel.pane ? locateSlot(live.tree, sel.pane)?.pane.uid || "" : "");
  const request = $derived(
    kind === "claude"
      ? { provider: kind, anchorPane: anchor, cwdFrom: "pane", payload, model, effort }
      : { provider: kind, anchorPane: anchor, cwdFrom: "pane", payload },
  );
  $effect(() => {
    if (!sel.project || !sel.window || !anchor) return;
    const body = request;
    post<{ argv: string[] }>(paths.agentPreview(sel.project, sel.window), body)
      .then((res) => {
        preview = res.argv.join(" ");
        previewError = "";
      })
      .catch((err) => {
        preview = "";
        previewError = explain(err).text;
      });
  });

  async function create() {
    if (!sel.project || !sel.window) return;
    creating = true;
    try {
      const body = await post<{ agent?: Agent; pane?: Pane }>(paths.windowAgents(sel.project, sel.window), {
        ...request,
        confirm: true,
      });
      await refresh();
      onClose();
      // tmux puts the new pane beside the one it split, and so does this.
      const ref = body.agent?.metadata.uid || body.pane?.metadata.uid;
      if (ref) setExtras([...route.extras, ref]);
    } catch (err) {
      fail(err);
    } finally {
      creating = false;
    }
  }
</script>

<Picker title={t("web.picker.beside")} hint="Alt-7" {onClose}>
  <input
    class="picker-query"
    placeholder={t("web.picker.filter")}
    bind:this={input}
    bind:value={query}
    onkeydown={(e) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
      if (e.key === "Enter" && candidates[0]) {
        e.preventDefault();
        beside(candidates[0]);
      }
    }}
  />
  <div class="picker-list">
    {#each candidates as row, i (row.pane.uid)}
      <button type="button" class="picker-row" class:selected={i === 0} onclick={() => beside(row)}>
        <span class="name">{paneLabel(row.pane).name}</span>
        <span class="where">{row.project.name} · {row.win.name}</span>
        {#if row.pane.agent}<span class="tag agent">{providerText(row.pane.agent.provider)}</span>{/if}
      </button>
    {:else}
      <div class="empty">{t("web.picker.none")}</div>
    {/each}
  </div>

  {#if resumable.length}
    <div class="picker-create">
      <div class="picker-sub">{t("web.picker.resume")}</div>
      {#each resumable as candidate (candidate.uid)}
        <button type="button" class="picker-row resume" disabled={!!resuming} onclick={() => resume(candidate)}>
          <div class="resume-head">
            <span class="name">{candidate.name === candidate.uid ? providerText(candidate.provider) : candidate.name}</span>
            <span class="where">
              {[providerText(candidate.provider), phaseText(candidate.phase), candidate.turns ? t("web.picker.turns", { n: candidate.turns }) : "", ago(candidate.at)]
                .filter(Boolean)
                .join(" · ")}
            </span>
          </div>
          {#if candidate.opening}<div class="resume-line open">{candidate.opening}</div>{/if}
          {#if candidate.last && candidate.last !== candidate.opening}<div class="resume-line last">{candidate.last}</div>{/if}
          {#if candidate.note}
            <div class="resume-line note" title={candidate.note}>
              {candidate.note === "empty" ? t("web.picker.note.empty") : t("web.chat.no_transcript")}
            </div>
          {/if}
        </button>
      {/each}
    </div>
  {/if}

  <div class="picker-create">
    <div class="picker-sub">{t("web.picker.create")}</div>
    {#if !anchor}
      <div class="empty">{t("web.picker.pick_pane_first")}</div>
    {:else}
      <div class="picker-kinds">
        {#each kinds as option (option)}
          <button type="button" class="picker-kind" aria-pressed={kind === option} onclick={() => (kind = option)}>{option}</button>
        {/each}
      </div>
      {#if kind === "claude"}
        <div class="picker-options">
          <label>
            {t("web.picker.model")}
            <select bind:value={model}>
              {#each claudeModels as option (option)}<option value={option}>{option || t("web.picker.default")}</option>{/each}
            </select>
          </label>
          <label>
            {t("web.picker.effort")}
            <select bind:value={effort}>
              {#each claudeEfforts as option (option)}<option value={option}>{option || t("web.picker.default")}</option>{/each}
            </select>
          </label>
        </div>
      {/if}
      <input class="picker-query payload" placeholder={t("web.picker.payload")} bind:value={payload} />
      <pre class="picker-preview">{preview || previewError}</pre>
      {#if preview}
        <button type="button" class="send" disabled={creating} onclick={create}>
          {creating ? t("web.picker.running") : t("web.picker.run")}
        </button>
      {/if}
    {/if}
  </div>
</Picker>
