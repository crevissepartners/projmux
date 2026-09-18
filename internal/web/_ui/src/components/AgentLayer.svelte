<script lang="ts">
  // One layer over the page for reading about Agents: an Agent's transcript,
  // or the messages two Agents sent each other. It only reads. It closes on
  // Escape, on a click outside it, and on its ×. An Agent that is live in a
  // Window also gets a way to that Window, focused on its slot.
  //
  // A message body is shown as the plain text it is, line breaks kept. It is
  // what one Agent wrote to another, so it is never read as markup.
  import { onDestroy } from "svelte";
  import { get, paths } from "../lib/api";
  import { explain, phaseText, providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { go } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { fullTime, shortTime } from "../lib/time";
  import { agentTitle, locateSlot, onlineOf, slotRef, type AgentRecord, type AgentView } from "../lib/tree";
  import type { LayerTarget, PeerMessage, PeerMessages } from "../lib/types";
  import AgentBadges from "./AgentBadges.svelte";
  import Chat from "./Chat.svelte";

  let { target, onClose }: { target: LayerTarget; onClose: () => void } = $props();

  const records = $derived(new Map(live.tree.agents.map((a) => [a.uid, a])));
  const record = (uid: string): AgentRecord | null => records.get(uid) || null;
  const nameOf = (uid: string) => agentTitle(record(uid), uid).name;

  // Chat reads an Agent's transcript by uid; an Agent the graph does not have
  // still gets a view of its uid, and the server says whether it has one.
  function viewOf(uid: string): AgentView {
    return (
      record(uid) || {
        uid,
        name: uid,
        provider: "",
        phase: "",
        reason: "",
        activity: "",
        activityAt: "",
        paneUID: "",
        windowUID: "",
        cwd: "",
      }
    );
  }

  // Where an online Agent is running: its live slot, if a Window holds one.
  // An offline Agent, or one no live pane holds, has nowhere to go.
  const slot = $derived.by(() => {
    if (target.kind !== "agent" || onlineOf(record(target.uid)) !== "online") return null;
    const found = locateSlot(live.tree, target.uid);
    return found?.pane.runtimeId ? found : null;
  });

  function goToWindow() {
    if (!slot) return;
    const to = { project: slot.project.uid, window: slot.win.uid, pane: slotRef(slot.pane) };
    onClose();
    go(to);
  }

  let panel: HTMLElement | undefined = $state();
  $effect(() => panel?.focus());

  // The pair's messages, read once per pair.
  let messages = $state<PeerMessage[]>([]);
  let loading = $state(false);
  let error = $state<{ text: string; detail: string } | null>(null);
  let reading: AbortController | null = null;

  async function readPair(a: string, b: string) {
    reading?.abort();
    const mine = new AbortController();
    reading = mine;
    loading = true;
    error = null;
    messages = [];
    try {
      const body = await get<PeerMessages>(paths.peerMessages(a, b), mine.signal);
      if (reading !== mine) return;
      // Oldest first, as the server sends them; kept stable for equal times.
      messages = (body.messages || [])
        .map((m, i) => ({ m, i }))
        .sort((x, y) => x.m.acceptedAt.localeCompare(y.m.acceptedAt) || x.i - y.i)
        .map(({ m }) => m);
    } catch (err) {
      if (reading !== mine) return;
      error = explain(err);
    }
    loading = false;
  }

  $effect(() => {
    if (target.kind === "pair") readPair(target.a, target.b);
  });
  onDestroy(() => reading?.abort());

  // Counts come from the graph's edge when the opener has it, else from the
  // messages read here.
  const counts = $derived.by(() => {
    if (target.kind !== "pair") return null;
    if (target.edge) return { ab: target.edge.aToB, ba: target.edge.bToA, last: target.edge.lastAcceptedAt };
    const { a } = target;
    const ab = messages.filter((m) => m.source === a).length;
    return { ab, ba: messages.length - ab, last: messages.at(-1)?.acceptedAt || "" };
  });

  function stateText(state: string): string {
    const key = `web.message.${state}`;
    const text = t(key);
    return text === key ? state : text;
  }

  function keydown(event: KeyboardEvent) {
    if (event.key !== "Escape") return;
    event.preventDefault();
    event.stopPropagation();
    onClose();
  }
</script>

<svelte:window onkeydown={keydown} />

<div
  class="layer-overlay"
  role="presentation"
  onmousedown={(e) => {
    if (e.target === e.currentTarget) onClose();
  }}
>
  <div class="layer" role="dialog" aria-modal="true" tabindex="-1" bind:this={panel}>
    <div class="layer-head">
      <div class="layer-title">
        {#if target.kind === "agent"}
          {@const rec = record(target.uid)}
          {@const title = agentTitle(rec, target.uid)}
          <span class="layer-kind">{t("web.layer.transcript")}</span>
          <span class="layer-name" class:dim={title.dim} title={target.uid}>{title.name}</span>
          <span class="layer-badges" data-provider={rec?.provider}><AgentBadges record={rec} /></span>
          {#if rec}
            <span class="layer-sub"
              >{[providerText(rec.provider), rec.phase ? phaseText(rec.phase) : ""].filter(Boolean).join(" · ")}</span
            >
          {/if}
        {:else}
          <span class="layer-kind">{t("web.layer.messages")}</span>
          <span class="layer-name" title={target.a}>{nameOf(target.a)}</span>
          <span class="layer-arrow">↔</span>
          <span class="layer-name" title={target.b}>{nameOf(target.b)}</span>
          {#if counts}
            <span class="layer-sub">
              {t("web.layer.counts", { a: nameOf(target.a), b: nameOf(target.b), ab: counts.ab, ba: counts.ba })}
              {#if counts.last}
                · <span title={fullTime(counts.last)}>{t("web.layer.last", { time: shortTime(counts.last) })}</span>
              {/if}
            </span>
          {/if}
        {/if}
      </div>
      {#if slot}
        <button
          type="button"
          class="tbtn layer-go"
          title={t("web.layer.go_window_title", { project: slot.project.name, window: slot.win.name })}
          onclick={goToWindow}>{t("web.layer.go_window")}</button
        >
      {/if}
      <span class="tag">{t("web.layer.read_only")}</span>
      <button type="button" class="layer-close" title={t("web.layer.close")} aria-label={t("web.layer.close")} onclick={onClose}
        >×</button
      >
    </div>
    <div class="layer-body">
      {#if target.kind === "agent"}
        {#key target.uid}
          <Chat agent={viewOf(target.uid)} paneUID={record(target.uid)?.paneUID || ""} readonly />
        {/key}
      {:else if loading}
        <div class="notice">{t("web.status.loading")}</div>
      {:else if error}
        <div class="notice err">
          {error.text}
          <details class="toast-detail"><summary>{t("web.error.details")}</summary><pre>{error.detail}</pre></details>
        </div>
      {:else if !messages.length}
        <div class="notice">{t("web.layer.no_messages")}</div>
      {:else}
        <ol class="peer-log">
          {#each messages as message (message.messageRef)}
            <li class="peer-msg {message.direction}">
              <div class="peer-msg-head">
                <span class="peer-dir">{nameOf(message.source)} → {nameOf(message.target)}</span>
                {#if message.replyTo}<span class="flag">{t("web.layer.reply")}</span>{/if}
                <span class="tag peer-state {message.state}">{stateText(message.state)}</span>
                <span class="when" title={fullTime(message.acceptedAt)}>{shortTime(message.acceptedAt)}</span>
              </div>
              {#if message.bodyRetained && message.payload !== undefined}
                <div class="peer-body">{message.payload}</div>
              {:else}
                <div class="peer-body gone">{t("web.layer.not_retained", { n: message.payloadBytes })}</div>
              {/if}
            </li>
          {/each}
        </ol>
      {/if}
    </div>
  </div>
</div>
