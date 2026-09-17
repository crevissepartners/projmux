<script lang="ts">
  // The conversation. The tail is read once and then followed from where the
  // read ended, over the page's one transcript stream, so a reply appears as
  // the provider writes it and nothing is rendered twice.
  import { onDestroy, untrack } from "svelte";
  import { get, paths } from "../lib/api";
  import { stickToBottom } from "../lib/actions";
  import { explain, providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import type { AgentView } from "../lib/tree";
  import type { Repository, Surface, TranscriptView, Turn } from "../lib/types";
  import Composer from "./Composer.svelte";
  import { withImages } from "../lib/uploads";
  import Pasted from "./Pasted.svelte";
  import TurnView from "./TurnView.svelte";
  import { PENDING_LATE_MS, pending, settle } from "../lib/pending.svelte";
  import { followTranscript } from "../lib/transcripts";

  interface Props {
    agent: AgentView;
    paneUID: string;
    stream?: "" | "live" | "warn";
    /** The model and effort the provider last recorded running with. */
    model?: { model: string; effort: string } | null;
  }
  let { agent, paneUID, stream = $bindable(""), model = $bindable(null) }: Props = $props();

  function noteModel(turn: Turn) {
    if (turn.model) model = { model: turn.model, effort: turn.effort || "" };
  }

  let turns = $state<Turn[]>([]);
  let note = $state("");
  let truncated = $state(false);
  let error = $state<{ text: string; detail: string } | null>(null);
  let loading = $state(true);
  let repo = $state<Repository | null>(null);
  let surface = $state<Surface | null>(null);
  let log: HTMLElement | undefined = $state();
  // The slot is rebuilt with {#key}, so this instance can be destroyed while
  // its read is still out. Nothing may follow, or write to the parent's
  // bindings, after that.
  let destroyed = false;
  const reading = new AbortController();
  let unfollow: (() => void) | null = null;

  // A reply can be recorded before the send that caused it returns, so a
  // pending line the transcript already shows is not drawn.
  const waiting = $derived(
    pending.filter(
      (p) =>
        p.agent === agent.uid &&
        !turns.some(
          (turn) =>
            (p.messageRef && turn.messageRef === p.messageRef) ||
            (turn.role === "user" && turn.text.trim() === p.text.trim()),
        ),
    ),
  );
  const agentName = $derived(agent.name && agent.name !== agent.uid ? agent.name : providerText(agent.provider));
  // A turn whose only content is the reasoning flag says nothing a reader can
  // use, and on a busy session it is every other row.
  const noise = (turn: Turn) => turn.thinking && !turn.text.trim() && !turn.tools?.length;
  const speaker = (turn: Turn) =>
    turn.task ? `task:${turn.at}` : `${turn.role}:${turn.via || ""}:${turn.from?.agentUID || ""}:${turn.report ? "report" : ""}`;

  // A streamed tool result arrives as its own record, keyed by the id of the
  // call it answers. The full read stitches those server-side; the stream
  // does it here, so the two paths show the same thing.
  function merge(turn: Turn): Turn | null {
    const calls = (turn.tools || []).filter((call) => {
      if (call.name || !call.id) return true;
      for (let i = turns.length - 1; i >= 0; i--) {
        const target = turns[i].tools?.find((c) => c.name && c.id === call.id);
        if (target) {
          target.result = call.result;
          target.error = call.error;
          target.clipped = target.clipped || call.clipped;
          return false;
        }
      }
      return true;
    });
    const merged = { ...turn, tools: calls };
    if (!merged.text.trim() && !calls.length && !merged.thinking && !merged.task && !merged.images) return null;
    return merged;
  }

  async function start(uid: string) {
    let offset = -1;
    try {
      const body = await get<TranscriptView>(paths.transcript(uid), reading.signal);
      if (destroyed) return;
      offset = body.transcript.offset;
      const all = body.transcript.turns || [];
      all.forEach(noteModel);
      turns = all.filter((turn) => !noise(turn) && turn.kind !== "context");
      for (const turn of turns) settle(uid, turn);
      note = body.transcript.note || "";
      truncated = body.transcript.truncated;
      repo = body.repository || null;
      surface = body.surface;
    } catch (err) {
      if (destroyed) return;
      error = explain(err);
      loading = false;
      return;
    }
    loading = false;
    if (note === "no-transcript") return;
    // The stream starts where this read ended, so nothing written in between
    // is lost; a reconnect resumes from where the stream got to.
    unfollow = followTranscript(uid, offset, {
      state: (state) => {
        if (!destroyed) stream = state;
      },
      turn: (raw) => {
        if (destroyed) return;
        settle(uid, raw);
        noteModel(raw);
        if (raw.kind === "context") return;
        const turn = merge(raw);
        if (turn && !noise(turn)) {
          turns.push(turn);
          note = "";
        }
      },
    });
  }

  // The slot is rebuilt when its agent changes, so one start per instance is right.
  start(untrack(() => agent.uid));

  // Pending lines are always drawn after the recorded turns, so they never
  // hide one; this clock only changes what a late one says.
  let now = $state(Date.now());
  const clock = setInterval(() => (now = Date.now()), 5000);

  onDestroy(() => {
    destroyed = true;
    reading.abort();
    unfollow?.();
    unfollow = null;
    clearInterval(clock);
    stream = "";
  });

  function toEnd() {
    if (log) log.scrollTop = log.scrollHeight;
  }
</script>

<!-- Log first and growing, the composer right under it and pinned: a
     composer that scrolls with the log ends up mid-slot. -->
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
          <TurnView {turn} {agentName} {repo} {paneUID} agentUID={agent.uid} continued={i > 0 && speaker(turns[i - 1]) === speaker(turn)} />
        {/each}
        {#each waiting as item (item.id)}
          {@const shown = withImages(item.text)}
          <div class="turn user pending">
            <div class="who">
              <span>{t("web.chat.me")}</span>
              <span class="flag">{now - item.sentAt > PENDING_LATE_MS ? t("web.chat.pending_late") : t("web.chat.pending")}</span>
            </div>
            <div class="body">{shown.text}</div>
            {#if shown.images.length}<Pasted images={shown.images} />{/if}
          </div>
        {/each}
      {/if}
    </div>
  </div>
</div>
<div class="slot-foot">
  {#if surface}
    <Composer {agent} {paneUID} {surface} onSent={toEnd} />
  {/if}
</div>
