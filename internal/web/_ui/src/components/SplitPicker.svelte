<script lang="ts">
  // Alt-7: open an existing pane beside this one, resume an Offline agent, or
  // split a real tmux Pane. The first is a view and free; the other two start
  // provider sessions that spend quota, so creating never runs on one click:
  // the exact command is fetched from the builder the server runs and shown
  // before the create button appears.
  import { untrack } from "svelte";
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
  // A candidate with no transcript has nothing to recognize it by; those are
  // folded away behind one line.
  let showEmpty = $state(false);
  const matches = (c: ResumeCandidate) =>
    !query.trim() ||
    `${c.name} ${c.provider} ${c.opening || ""} ${c.last || ""}`.toLowerCase().includes(query.trim().toLowerCase());
  const hasHistory = (c: ResumeCandidate) => !!(c.opening || c.last) || c.note === "empty";
  const withHistory = $derived(resumable.filter(hasHistory));
  const withoutHistory = $derived(resumable.filter((c) => !hasHistory(c)));
  const shownResume = $derived([...withHistory, ...(showEmpty ? withoutHistory : [])].filter(matches));
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
  // The picker opens on what is most likely wanted: an existing pane when
  // there is one, a new one otherwise.
  let tab = $state<"open" | "resume" | "create">(untrack(() => (candidates.length ? "open" : "create")));
  $effect(() => {
    if (tab !== "create") input?.focus();
  });
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

<Picker title={t("web.picker.title")} hint="Alt-7" {onClose}>
  <div class="picker-tabs" role="tablist">
    <button type="button" role="tab" aria-selected={tab === "open"} onclick={() => (tab = "open")}>
      {t("web.picker.beside")}<span class="count">{candidates.length}</span>
    </button>
    {#if resumable.length}
      <button type="button" role="tab" aria-selected={tab === "resume"} onclick={() => (tab = "resume")}>
        {t("web.picker.resume")}<span class="count">{withHistory.length}</span>
      </button>
    {/if}
    <button type="button" role="tab" aria-selected={tab === "create"} onclick={() => (tab = "create")}>
      {t("web.picker.new")}
    </button>
  </div>

  {#if tab !== "create"}
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
        if (e.key === "Enter") {
          e.preventDefault();
          if (tab === "open" && candidates[0]) beside(candidates[0]);
          if (tab === "resume" && shownResume[0]) resume(shownResume[0]);
        }
      }}
    />
  {/if}

  {#if tab === "open"}
    <div class="picker-list">
      {#each candidates as row, i (row.pane.uid)}
        <button type="button" class="picker-row" class:selected={i === 0} onclick={() => beside(row)}>
          <span class="name">{paneLabel(row.pane).name}</span>
          <!-- An unnamed Window's name is its uid, which is not a label. -->
          <span class="where">{row.win.name && row.win.name !== row.win.uid ? `${row.project.name} · ${row.win.name}` : row.project.name}</span>
          {#if row.pane.agent}<span class="tag agent">{providerText(row.pane.agent.provider)}</span>{/if}
        </button>
      {:else}
        <div class="empty">{t("web.picker.none")}</div>
      {/each}
    </div>
  {:else if tab === "resume"}
    <div class="picker-list">
      {#each shownResume as candidate, i (candidate.uid)}
        <button
          type="button"
          class="picker-row resume"
          class:selected={i === 0}
          disabled={!!resuming}
          onclick={() => resume(candidate)}
        >
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
          {#if candidate.note === "empty"}<div class="resume-line note">{t("web.picker.note.empty")}</div>{/if}
        </button>
      {:else}
        <div class="empty">{t("web.picker.none")}</div>
      {/each}
      {#if withoutHistory.length}
        <button type="button" class="picker-more" onclick={() => (showEmpty = !showEmpty)}>
          {showEmpty ? t("web.picker.hide_empty") : t("web.picker.show_empty", { n: withoutHistory.length })}
        </button>
      {/if}
    </div>
  {:else}
    <div class="picker-create">
      {#if !anchor}
        <div class="empty">{t("web.picker.pick_pane_first")}</div>
      {:else}
        <div class="picker-field">
          <span class="label">{t("web.picker.kind")}</span>
          <div class="picker-kinds">
            {#each kinds as option (option)}
              <button type="button" class="picker-kind" aria-pressed={kind === option} onclick={() => (kind = option)}
                >{providerText(option)}</button
              >
            {/each}
          </div>
        </div>
        {#if kind === "claude"}
          <div class="picker-field">
            <span class="label">{t("web.picker.model")}</span>
            <div class="picker-kinds">
              {#each claudeModels as option (option)}
                <button type="button" class="picker-kind" aria-pressed={model === option} onclick={() => (model = option)}
                  >{option || t("web.picker.default")}</button
                >
              {/each}
            </div>
          </div>
          <div class="picker-field">
            <span class="label">{t("web.picker.effort")}</span>
            <div class="picker-kinds">
              {#each claudeEfforts as option (option)}
                <button type="button" class="picker-kind" aria-pressed={effort === option} onclick={() => (effort = option)}
                  >{option || t("web.picker.default")}</button
                >
              {/each}
            </div>
          </div>
        {/if}
        <label class="picker-field">
          <span class="label">{t("web.picker.first")}</span>
          <input
            class="payload"
            placeholder={t("web.picker.payload")}
            bind:value={payload}
            onkeydown={(e) => {
              if (e.key === "Enter" && preview && !creating) {
                e.preventDefault();
                create();
              }
            }}
          />
        </label>
        <div class="picker-field">
          <span class="label">{t("web.picker.command")}</span>
          <pre class="picker-preview">{preview || previewError}</pre>
        </div>
        <div class="picker-actions">
          <button type="button" class="send" disabled={creating || !preview} onclick={create}>
            {creating ? t("web.picker.running") : t("web.picker.run")}
          </button>
        </div>
      {/if}
    </div>
  {/if}
</Picker>
