<script lang="ts">
  import { providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { markdown } from "../lib/markdown";
  import { go } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { paneLabel, slotRef } from "../lib/tree";
  import { fullTime, shortTime } from "../lib/time";
  import type { Repository, Turn } from "../lib/types";
  import { withImages } from "../lib/uploads";
  import Pasted from "./Pasted.svelte";
  import TaskLine from "./TaskLine.svelte";
  import ToolView from "./ToolView.svelte";

  interface Props {
    turn: Turn;
    agentName: string;
    repo: Repository | null;
    continued: boolean;
    paneUID: string;
    agentUID: string;
  }
  let { turn, agentName, repo, continued, paneUID, agentUID }: Props = $props();

  // The label says who: the operator's own messages read as "me", the
  // agent's as its name.
  const label = $derived(
    turn.report
      ? t("web.chat.report")
      : turn.role === "user"
      ? t("web.chat.me")
      : turn.role === "assistant"
        ? agentName || turn.role
        : turn.role === "peer"
          ? t("web.chat.peer")
          : turn.role,
  );

  // A peer with a live pane is a link to that pane's slot. It changes only
  // what this page shows, not the operator's tmux focus.
  const peerTarget = $derived.by(() => {
    const uid = turn.from?.agentUID;
    if (!uid) return null;
    for (const project of live.tree.projects) {
      for (const win of project.windows) {
        const pane = win.panes.find((p) => p.agent?.uid === uid && p.runtimeId);
        if (pane) return { project: project.uid, window: win.uid, pane: slotRef(pane), name: paneLabel(pane).name };
      }
    }
    return null;
  });

  // Only what a person or a peer sent carries pasted images; an agent that
  // mentions an upload path is quoting it.
  const shown = $derived(
    turn.role === "user" || turn.from || turn.via ? withImages(turn.text) : { text: turn.text, images: [] },
  );

  const peerName = $derived.by(() => {
    if (!turn.from) return "";
    return peerTarget?.name || providerText(turn.from.provider || "");
  });
</script>

{#if turn.task}
  <TaskLine task={turn.task} at={turn.at} />
{:else}
<div class="turn {turn.role}" class:cont={continued} class:report={!!turn.report}>
  {#if !continued}
    <div class="who">
      <span>{label}</span>
      {#if turn.from}
        {#if peerTarget}
          <button
            type="button"
            class="peer-link"
            title={t("web.slot.peer_link")}
            onclick={() => go({ project: peerTarget.project, window: peerTarget.window, pane: peerTarget.pane })}
            >{peerName}</button
          >
        {:else}
          <span>{peerName}</span>
        {/if}

      {/if}
      {#if turn.via}<span class="via">{turn.via}</span>{/if}
      {#if turn.at}<span class="when" title={fullTime(turn.at)}>{shortTime(turn.at)}</span>{/if}
    </div>
  {/if}
  {#if turn.images}
    <div class="body"><span class="flag">{t("web.chat.images", { n: turn.images })}</span></div>
  {/if}
  {#if shown.text.trim() && turn.report}
    <details class="report-body" open>
      <summary>{t("web.chat.report_summary")}</summary>
      <div class="body" use:markdown={{ text: shown.text.trim(), repo }}></div>
    </details>
  {:else if shown.text.trim()}
    <div class="body" use:markdown={{ text: shown.text.trim(), repo }}></div>
  {:else if turn.thinking && !turn.tools?.length}
    <div class="body"><span class="flag">· {t("web.chat.thinking")}</span></div>
  {/if}
  {#if shown.images.length}<Pasted images={shown.images} />{/if}
  {#each turn.tools || [] as call, i (call.id || i)}
    <ToolView {call} {paneUID} {agentUID} />
  {/each}
</div>
{/if}
