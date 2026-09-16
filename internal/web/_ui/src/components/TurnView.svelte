<script lang="ts">
  import { providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { markdown } from "../lib/markdown";
  import { go } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { paneLabel } from "../lib/tree";
  import { fullTime, shortTime } from "../lib/time";
  import type { Repository, Turn } from "../lib/types";
  import ToolView from "./ToolView.svelte";

  interface Props {
    turn: Turn;
    agentName: string;
    repo: Repository | null;
    continued: boolean;
  }
  let { turn, agentName, repo, continued }: Props = $props();

  // The label says who: the operator's own messages read as "me", the
  // agent's as its name.
  const label = $derived(
    turn.role === "user"
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
        if (pane) return { project: project.uid, window: win.uid, pane: pane.uid, name: paneLabel(pane).name };
      }
    }
    return null;
  });

  const peerName = $derived.by(() => {
    if (!turn.from) return "";
    return peerTarget?.name || providerText(turn.from.provider || "");
  });
</script>

<div class="turn {turn.role}" class:cont={continued}>
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
  {#if turn.text.trim()}
    <div class="body" use:markdown={{ text: turn.text.trim(), repo }}></div>
  {:else if turn.thinking && !turn.tools?.length}
    <div class="body"><span class="flag">· {t("web.chat.thinking")}</span></div>
  {/if}
  {#each turn.tools || [] as call, i (call.id || i)}
    <ToolView {call} />
  {/each}
</div>
