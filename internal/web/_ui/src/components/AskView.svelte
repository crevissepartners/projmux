<script lang="ts">
  // An AskUserQuestion, shown as the question it is. The web client does not
  // answer it: projmux has no answer channel yet and keeps agent input off
  // raw pane keys, so the card says to answer in the terminal.
  import { t } from "../lib/i18n.svelte";

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
  import OpenInTerminal from "./OpenInTerminal.svelte";

  let { questions, result, paneUID }: { questions: unknown[]; result: string; paneUID: string } = $props();
  const list = $derived(questions as Question[]);
</script>

<div class="ask" class:answered={!!result}>
  <div class="ask-head"><span>{result ? t("web.chat.answered") : t("web.chat.question")}</span></div>
  {#each list as q, qi (qi)}
    <div class="ask-q">
      {#if q.header}<span class="ask-tag">{q.header}</span>{/if}
      <div class="ask-text">{q.question || ""}</div>
      {#each q.options || [] as option (option.label)}
        <div class="ask-opt">
          <span class="ask-body">
            <span class="ask-label">{option.label}</span>
            {#if option.description}<span class="ask-desc">{option.description}</span>{/if}
          </span>
          {#if option.preview}<pre class="ask-preview">{option.preview}</pre>{/if}
        </div>
      {/each}
    </div>
  {/each}
  {#if result}
    <pre class="ask-result">{result}</pre>
  {:else}
    <div class="ask-foot">
      <OpenInTerminal {paneUID} />
      <span class="ask-note">{t("web.chat.answer_in_terminal")}</span>
    </div>
  {/if}
</div>
