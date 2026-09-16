<script lang="ts">
  import { untrack } from "svelte";
  import { ApiError, del, paths, post } from "../lib/api";
  import { deliveryText, explain } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { drop, load, save } from "../lib/local";
  import { addPending } from "../lib/pending.svelte";
  import type { AgentView } from "../lib/tree";
  import type { Surface } from "../lib/types";
  import { ui } from "../lib/ui.svelte";

  interface Props {
    agent: AgentView;
    paneUID: string;
    surface: Surface;
    onSent: () => void;
  }
  let { agent, paneUID, surface, onSent }: Props = $props();

  // An unsent draft belongs to this browser and this agent, and is the one
  // thing a refresh must not throw away: the person typed it.
  const draftKey = $derived(`draft.${agent.uid}`);
  // The slot is rebuilt when its agent changes, so reading the first uid once is right.
  let text = $state(load(untrack(() => `draft.${agent.uid}`), ""));
  let sending = $state(false);
  let receipt = $state<{ text: string; detail?: string; err: boolean } | null>(null);
  let input: HTMLTextAreaElement | undefined = $state();

  // A message is sent as the target agent itself: a browser has no pane of
  // its own to send from, and naming the target keeps a person's text from
  // being attributed to some other agent. The agent has to be running to
  // take it.
  const running = $derived(agent.phase === "Running");

  const bytes = $derived(new TextEncoder().encode(text).length);
  const over = $derived(!!surface.maxBytes && bytes > surface.maxBytes);
  const blocked = $derived(!!surface.sourceRequired && !running);
  const turnMode = $derived(surface.mode === "turn");

  // The box grows with what is typed instead of reserving lines for a message
  // that is usually one.
  function fit() {
    if (!input) return;
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight + 2, 240)}px`;
  }

  function edited() {
    fit();
    if (text) save(draftKey, text);
    else drop(draftKey);
  }

  // Focusing a pane puts the caret here, because typing is what comes next.
  $effect(() => {
    if (ui.focusComposer === paneUID && input && !input.disabled) {
      ui.focusComposer = "";
      input.focus({ preventScroll: true });
    }
  });
  $effect(() => {
    fit();
  });

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (!text.trim() || over || blocked || sending) return;
    sending = true;
    receipt = null;
    try {
      if (turnMode) {
        try {
          await post(`${paths.agent(agent.uid)}/turns`, { text });
          receipt = { text: t("web.composer.started"), err: false };
          addPending(agent.uid, text);
        } catch (err) {
          // A running turn refuses a start. Adding to it is what sending
          // means then, and it is this client's call to make, not the server's.
          if (!(err instanceof ApiError && err.code === "turn-in-progress")) throw err;
          await post(`${paths.agent(agent.uid)}/turns/current/steer`, { text });
          receipt = { text: t("web.composer.steered"), err: false };
          addPending(agent.uid, text);
        }
      } else {
        const body = await post<{ delivery: { state: string; messageRef?: string } }>(
          `${paths.agent(agent.uid)}/messages`,
          { body: text, source: agent.uid },
        );
        receipt = { text: deliveryText(body.delivery.state), err: false };
        addPending(agent.uid, text, body.delivery.messageRef || "");
      }
      text = "";
      drop(draftKey);
      onSent();
    } catch (err) {
      receipt = { ...explain(err), err: true };
    } finally {
      sending = false;
    }
  }

  async function stop() {
    try {
      await del(`${paths.agent(agent.uid)}/turns/current`);
      receipt = { text: t("web.composer.stopped"), err: false };
    } catch (err) {
      receipt = { ...explain(err), err: true };
    }
  }
</script>

{#if surface.mode === "none"}
  <div class="notice warn">{t("web.composer.no_input")}</div>
{:else}
  <form class="composer" onsubmit={submit}>
    {#if blocked}<div class="notice warn">{t("web.composer.not_running")}</div>{/if}
    <textarea
      bind:this={input}
      bind:value={text}
      oninput={edited}
      disabled={blocked}
      placeholder={turnMode ? t("web.composer.turn_placeholder") : t("web.composer.message_placeholder")}
      title={turnMode ? "" : t("web.composer.utterance_note")}
      onkeydown={(e) => {
        if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
          e.preventDefault();
          (e.currentTarget as HTMLTextAreaElement).form?.requestSubmit();
        }
      }}
    ></textarea>
    <div class="controls">
      <button type="submit" class="send" disabled={sending || over || blocked || !text.trim()}>
        {sending ? t("web.composer.sending") : turnMode ? t("web.composer.send_turn") : t("web.composer.send_message")}
      </button>
      {#if surface.canStop}
        <button type="button" class="stop" onclick={stop}>{t("web.composer.stop")}</button>
      {/if}
      <span class="count" class:over>{surface.maxBytes ? `${bytes} / ${surface.maxBytes} B` : `${bytes} B`}</span>
    </div>
    {#if receipt}
      <div class="notice receipt" class:err={receipt.err}>
        {receipt.text}
        {#if receipt.detail && receipt.detail !== receipt.text}
          <details class="toast-detail">
            <summary>{t("web.error.details")}</summary>
            <pre>{receipt.detail}</pre>
          </details>
        {/if}
      </div>
    {/if}
  </form>
{/if}
