<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import { ApiError, UPLOAD_LIMIT, UPLOAD_TYPES, del, paths, post, upload } from "../lib/api";
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

  // A pasted or dropped image becomes an `[Image #N]` mark where the caret
  // was, as in Claude Code. The image is uploaded right away, and on send
  // each mark is replaced by the stored file's path, which is how a provider
  // reads an image. A mark the person deleted takes its image with it.
  interface Attachment {
    n: number;
    preview: string;
    path: string;
    error: string;
  }
  let attachments = $state<Attachment[]>([]);
  let nextImage = 1;
  const mark = (n: number) => `[Image #${n}]`;
  const used = $derived(attachments.filter((a) => text.includes(mark(a.n))));
  const uploading = $derived(used.some((a) => !a.path && !a.error));
  const failed = $derived(used.some((a) => a.error));
  const composed = $derived(
    used.reduce((out, a) => (a.path ? out.replaceAll(mark(a.n), a.path) : out), text),
  );

  // A message is sent as the target agent itself: a browser has no pane of
  // its own to send from, and naming the target keeps a person's text from
  // being attributed to some other agent. The agent has to be running to
  // take it.
  const running = $derived(agent.phase === "Running");

  const bytes = $derived(new TextEncoder().encode(composed).length);
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

  function attach(files: Iterable<File>) {
    const marks: string[] = [];
    for (const file of files) {
      const item: Attachment = { n: nextImage++, preview: URL.createObjectURL(file), path: "", error: "" };
      if (!UPLOAD_TYPES.includes(file.type)) item.error = t("web.composer.image_type");
      else if (file.size > UPLOAD_LIMIT) item.error = t("web.composer.image_size");
      attachments.push(item);
      marks.push(mark(item.n));
      if (item.error) continue;
      const n = item.n;
      upload(file).then(
        (stored) => update(n, { path: stored.path }),
        (err) => update(n, { error: explain(err).text }),
      );
    }
    if (marks.length) insertAtCaret(marks.join(" "));
  }

  function insertAtCaret(value: string) {
    const start = input?.selectionStart ?? text.length;
    const end = input?.selectionEnd ?? text.length;
    const before = text.slice(0, start);
    const after = text.slice(end);
    const lead = before && !/\s$/.test(before) ? " " : "";
    const trail = after && !/^\s/.test(after) ? " " : "";
    text = `${before}${lead}${value}${trail}${after}`;
    const caret = before.length + lead.length + value.length + trail.length;
    requestAnimationFrame(() => {
      input?.focus();
      input?.setSelectionRange(caret, caret);
    });
    edited();
  }

  function update(n: number, change: Partial<Attachment>) {
    const found = attachments.find((a) => a.n === n);
    if (found) Object.assign(found, change);
  }

  function detach(n: number) {
    text = text.replaceAll(mark(n), "").replace(/ {2,}/g, " ");
    edited();
  }

  function clearAttachments() {
    for (const a of attachments) URL.revokeObjectURL(a.preview);
    attachments = [];
    nextImage = 1;
  }
  onDestroy(() => untrack(clearAttachments));

  function imagesOf(list: DataTransferItemList | undefined | null): File[] {
    const files: File[] = [];
    for (const item of list || []) {
      if (item.kind !== "file" || !item.type.startsWith("image/")) continue;
      const file = item.getAsFile();
      if (file) files.push(file);
    }
    return files;
  }

  function pasted(event: ClipboardEvent) {
    const files = imagesOf(event.clipboardData?.items);
    // Text in the same paste still lands in the box; only an image-only
    // paste is taken over.
    if (!files.length) return;
    if (!event.clipboardData?.getData("text/plain")) event.preventDefault();
    attach(files);
  }

  let dragging = $state(false);
  function dragOver(event: DragEvent) {
    if (!event.dataTransfer?.types.includes("Files") || blocked) return;
    event.preventDefault();
    dragging = true;
  }
  function dropped(event: DragEvent) {
    dragging = false;
    if (!event.dataTransfer?.files.length || blocked) return;
    event.preventDefault();
    attach(event.dataTransfer.files);
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (!composed.trim() || over || blocked || sending || uploading || failed) return;
    const message = composed;
    sending = true;
    receipt = null;
    try {
      if (turnMode) {
        try {
          await post(`${paths.agent(agent.uid)}/turns`, { text: message });
          receipt = { text: t("web.composer.started"), err: false };
          addPending(agent.uid, message);
        } catch (err) {
          // A running turn refuses a start. Adding to it is what sending
          // means then, and it is this client's call to make, not the server's.
          if (!(err instanceof ApiError && err.code === "turn-in-progress")) throw err;
          await post(`${paths.agent(agent.uid)}/turns/current/steer`, { text: message });
          receipt = { text: t("web.composer.steered"), err: false };
          addPending(agent.uid, message);
        }
      } else {
        const body = await post<{ delivery: { state: string; messageRef?: string } }>(
          `${paths.agent(agent.uid)}/messages`,
          { body: message, source: agent.uid },
        );
        receipt = { text: deliveryText(body.delivery.state), err: false };
        addPending(agent.uid, message, body.delivery.messageRef || "");
      }
      clearDraft();
      onSent();
    } catch (err) {
      receipt = { ...explain(err), err: true };
    } finally {
      sending = false;
    }
  }

  function clearDraft() {
    text = "";
    drop(draftKey);
    clearAttachments();
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
  <form
    class="composer"
    class:dragging
    onsubmit={submit}
    ondragover={dragOver}
    ondragleave={() => (dragging = false)}
    ondrop={dropped}
  >
    {#if blocked}<div class="notice warn">{t("web.composer.not_running")}</div>{/if}
    <textarea
      bind:this={input}
      bind:value={text}
      oninput={edited}
      onpaste={pasted}
      title={turnMode ? t("web.composer.image_hint") : `${t("web.composer.utterance_note")}\n${t("web.composer.image_hint")}`}
      disabled={blocked}
      placeholder={turnMode ? t("web.composer.turn_placeholder") : t("web.composer.message_placeholder")}
      onkeydown={(e) => {
        if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) {
          e.preventDefault();
          (e.currentTarget as HTMLTextAreaElement).form?.requestSubmit();
        }
      }}
    ></textarea>
    {#if used.length}
      <ul class="attachments">
        {#each used as item (item.n)}
          <li class:failed={!!item.error} class:loading={!item.path && !item.error} title={item.error}>
            <img src={item.preview} alt={mark(item.n)} />
            <span class="attach-note">
              {item.error ? item.error : item.path ? `#${item.n}` : t("web.composer.image_uploading")}
            </span>
            <button
              type="button"
              class="attach-x"
              title={t("web.composer.image_remove")}
              aria-label={t("web.composer.image_remove")}
              onclick={() => detach(item.n)}>×</button
            >
          </li>
        {/each}
      </ul>
    {/if}
    <div class="controls">
      <button type="submit" class="send" disabled={sending || uploading || failed || over || blocked || !composed.trim()}>
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
