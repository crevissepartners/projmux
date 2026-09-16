<script lang="ts">
  // The conversation. The tail is read once and then followed with server-sent
  // events from the end of the file, so a reply appears as the provider writes
  // it and nothing is rendered twice.
  import { onDestroy, untrack, type Snippet } from "svelte";
  import { get, paths } from "../lib/api";
  import { stickToBottom } from "../lib/actions";
  import { explain, providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import type { AgentView } from "../lib/tree";
  import type { Repository, Surface, TranscriptView, Turn } from "../lib/types";
  import Composer from "./Composer.svelte";
  import TurnView from "./TurnView.svelte";

  interface Props {
    agent: AgentView;
    paneUID: string;
    /** The registry record, folded between the log and the composer. */
    record: Snippet;
    stream?: "" | "live" | "warn";
  }
  let { agent, paneUID, record, stream = $bindable("") }: Props = $props();

  let turns = $state<Turn[]>([]);
  let note = $state("");
  let truncated = $state(false);
  let error = $state<{ text: string; detail: string } | null>(null);
  let loading = $state(true);
  let repo = $state<Repository | null>(null);
  let surface = $state<Surface | null>(null);
  let log: HTMLElement | undefined = $state();
  let source: EventSource | null = null;

  // A turn whose only content is the reasoning flag says nothing a reader can
  // use, and on a busy session it is every other row.
  const agentName = $derived(agent.name && agent.name !== agent.uid ? agent.name : providerText(agent.provider));
  const noise = (turn: Turn) => turn.thinking && !turn.text.trim() && !turn.tools?.length;
  const speaker = (turn: Turn) => `${turn.role}:${turn.via || ""}:${turn.from?.agentUID || ""}`;

  async function start(uid: string) {
    try {
      const body = await get<TranscriptView>(paths.transcript(uid));
      turns = (body.transcript.turns || []).filter((turn) => !noise(turn));
      note = body.transcript.note || "";
      truncated = body.transcript.truncated;
      repo = body.repository || null;
      surface = body.surface;
    } catch (err) {
      error = explain(err);
      loading = false;
      return;
    }
    loading = false;
    if (note === "no-transcript") return;
    const events = new EventSource(`${paths.transcript(uid)}/events`);
    source = events;
    events.addEventListener("open", () => (stream = "live"));
    events.addEventListener("error", () => (stream = "warn"));
    events.addEventListener("turn", (event) => {
      try {
        const turn = JSON.parse((event as MessageEvent).data) as Turn;
        if (!noise(turn)) {
          turns.push(turn);
          note = "";
        }
      } catch {
        /* skip one malformed frame */
      }
    });
  }

  // The slot is rebuilt when its agent changes, so one start per instance is right.
  start(untrack(() => agent.uid));

  onDestroy(() => {
    source?.close();
    stream = "";
  });

  function toEnd() {
    if (log) log.scrollTop = log.scrollHeight;
  }
</script>

<!-- Log first and growing, the record folded under it, the composer last
     and pinned: a composer that scrolls with the log ends up mid-slot. -->
<div class="chat-area">
  <div class="chat">
    <div class="log" bind:this={log} use:stickToBottom>
      {#if loading}
        {t("web.status.loading")}
      {:else if error}
        <div class="notice err">
        {error.text}
        <details class="toast-detail"><summary>{t("web.error.details")}</summary><pre>{error.detail}</pre></details>
      </div>
      {:else}
        {#if truncated}<div class="notice">{t("web.chat.truncated")}</div>{/if}
        {#if !turns.length}
          <div class="notice">{note === "no-transcript" ? t("web.chat.no_transcript") : t("web.chat.empty")}</div>
        {/if}
        {#each turns as turn, i (i)}
          <TurnView {turn} agentName={agentName} {repo} continued={i > 0 && speaker(turns[i - 1]) === speaker(turn)} />
        {/each}
      {/if}
    </div>
  </div>
</div>
<details class="record-fold">
  <summary>{t("web.slot.record")}</summary>
  {@render record()}
</details>
<div class="slot-foot">
  {#if surface}
    <Composer {agent} {paneUID} {surface} onSent={toEnd} />
  {/if}
</div>
