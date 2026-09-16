<script lang="ts">
  // One tool call, collapsed to its name and the argument that identifies it.
  // Most of a working session is tool calls, so hiding them makes an agent
  // look idle; expanding them buries the prose. The summary is always visible
  // and the arguments and output are one click away.
  import { t } from "../lib/i18n.svelte";
  import type { ToolCall } from "../lib/types";
  import AskView from "./AskView.svelte";

  let { call, paneUID, agentUID }: { call: ToolCall; paneUID: string; agentUID: string } = $props();

  const question = $derived.by(() => {
    if (call.name !== "AskUserQuestion" || !call.input) return null;
    try {
      const parsed = JSON.parse(call.input) as { questions?: unknown };
      return Array.isArray(parsed.questions) && parsed.questions.length ? parsed.questions : null;
    } catch {
      return null;
    }
  });
</script>

{#if question}
  <AskView questions={question} result={call.result || ""} {paneUID} {agentUID} toolID={call.id || ""} />
{:else}
  <details class="tool" class:err={call.error}>
    <summary>
      <span class="tname">{call.name || t("web.chat.tool_result")}</span>
      {#if call.summary}<span class="tsum">{call.summary.replace(/\s+/g, " ").trim()}</span>{/if}
      {#if call.error}<span class="tflag">error</span>{/if}
      {#if call.clipped}<span class="tflag soft">{t("web.chat.clipped")}</span>{/if}
    </summary>
    {#if call.input}<pre class="tin">{call.input}</pre>{/if}
    {#if call.result}<pre class="tout">{call.result}</pre>{/if}
    {#if !call.input && !call.result}<div class="tempty">{t("web.chat.tool_empty")}</div>{/if}
  </details>
{/if}
