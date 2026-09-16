<script lang="ts">
  // An AskUserQuestion, rendered as the question it is and answerable here.
  //
  // The answer goes by option index: the server presses the widget's own
  // number keys after checking the question is still on screen, which is the
  // one place the web client puts keys into a pane. Free text on a
  // multi-select question was never measured and is left to the terminal,
  // which the card opens in one click.
  import { ApiError, paths, post } from "../lib/api";
  import { explain } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import OpenInTerminal from "./OpenInTerminal.svelte";

  interface Option {
    label: string;
    description?: string;
    preview?: string;
  }
  interface Question {
    header?: string;
    question?: string;
    multiSelect?: boolean;
    options?: Option[];
  }
  interface Props {
    questions: unknown[];
    result: string;
    paneUID: string;
    agentUID: string;
    toolID: string;
  }
  let { questions, result, paneUID, agentUID, toolID }: Props = $props();
  const list = $derived(questions as Question[]);

  let picks = $state<number[][]>([]);
  let others = $state<string[]>([]);
  $effect(() => {
    if (picks.length !== list.length) {
      picks = list.map(() => []);
      others = list.map(() => "");
    }
  });

  let sending = $state(false);
  let sent = $state(false);
  let refusal = $state<{ text: string; detail: string } | null>(null);

  function toggle(qi: number, oi: number, multi: boolean) {
    const current = picks[qi] || [];
    if (multi) {
      picks[qi] = current.includes(oi) ? current.filter((i) => i !== oi) : [...current, oi];
    } else {
      picks[qi] = [oi];
      others[qi] = "";
    }
  }

  function typed(qi: number) {
    // In the widget, typing is choosing the "Type something" row, so it
    // replaces a pick above it.
    if (!list[qi].multiSelect && others[qi].trim()) picks[qi] = [];
  }

  // Each question needs exactly what the widget accepts: one pick or free
  // text for single-select, at least one pick and no text for multi-select.
  const ready = $derived(
    list.length > 0 &&
      list.every((q, qi) => {
        const p = picks[qi] || [];
        const o = (others[qi] || "").trim();
        return q.multiSelect ? p.length > 0 && !o : (p.length === 1) !== !!o;
      }),
  );

  async function answer() {
    sending = true;
    refusal = null;
    try {
      await post(paths.question(agentUID), {
        toolId: toolID,
        answers: list.map((_, qi) => ({ picks: picks[qi], other: others[qi].trim() })),
      });
      sent = true;
    } catch (err) {
      refusal = explain(err);
      if (err instanceof ApiError && err.code === "question-answered") sent = true;
    } finally {
      sending = false;
    }
  }
</script>

<div class="ask" class:answered={!!result || sent}>
  <div class="ask-head"><span>{result || sent ? t("web.chat.answered") : t("web.chat.question")}</span></div>
  {#each list as q, qi (qi)}
    <div class="ask-q">
      {#if q.header}<span class="ask-tag">{q.header}</span>{/if}
      <div class="ask-text">{q.question || ""}</div>
      {#each q.options || [] as option, oi (oi)}
        <label class="ask-opt">
          <input
            type={q.multiSelect ? "checkbox" : "radio"}
            name="{toolID}-{qi}"
            disabled={!!result || sent}
            checked={(picks[qi] || []).includes(oi)}
            onchange={() => toggle(qi, oi, !!q.multiSelect)}
          />
          <span class="ask-body">
            <span class="ask-label">{option.label}</span>
            {#if option.description}<span class="ask-desc">{option.description}</span>{/if}
          </span>
          {#if option.preview}<pre class="ask-preview">{option.preview}</pre>{/if}
        </label>
      {/each}
      {#if !result && !sent}
        <input
          class="ask-other"
          placeholder={q.multiSelect ? t("web.chat.other_multi") : t("web.chat.other")}
          disabled={!!q.multiSelect}
          bind:value={others[qi]}
          oninput={() => typed(qi)}
        />
      {/if}
    </div>
  {/each}
  {#if result}
    <pre class="ask-result">{result}</pre>
  {:else if !sent}
    <div class="ask-foot">
      <button type="button" class="send" disabled={!ready || sending} onclick={answer}>
        {sending ? t("web.chat.answering") : t("web.chat.answer")}
      </button>
      <OpenInTerminal {paneUID} />
      {#if refusal}
        <span class="ask-note err" title={refusal.detail}>{refusal.text}</span>
      {:else}
        <span class="ask-note">{t("web.chat.answer_keys")}</span>
      {/if}
    </div>
  {/if}
</div>
