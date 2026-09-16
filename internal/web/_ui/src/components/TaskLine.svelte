<script lang="ts">
  // A background task finishing, as one line: what it was, how it ended, and
  // what it cost. The ids and the output file are one click away.
  import { t } from "../lib/i18n.svelte";
  import { shortTime, fullTime } from "../lib/time";
  import type { TaskNote } from "../lib/types";

  let { task, at }: { task: TaskNote; at?: string } = $props();

  const failed = $derived(
    ["failed", "killed", "error"].includes(task.status || "") || (task.exitCode !== undefined && task.exitCode !== 0),
  );
  const label = $derived(
    task.kind === "agent" ? t("web.task.agent") : task.kind === "command" ? t("web.task.command") : t("web.task.other"),
  );

  function duration(ms: number): string {
    const seconds = Math.round(ms / 1000);
    if (seconds < 60) return t("web.task.seconds", { n: seconds });
    return t("web.task.minutes", { m: Math.floor(seconds / 60), s: seconds % 60 });
  }

  function tokens(n: number): string {
    return n >= 1000 ? `${(n / 1000).toFixed(n >= 10_000 ? 0 : 1)}k` : String(n);
  }

  const facts = $derived(
    [
      task.exitCode !== undefined ? `exit ${task.exitCode}` : task.status && task.status !== "completed" ? task.status : "",
      task.toolUses ? t("web.task.tools", { n: task.toolUses }) : "",
      task.durationMs ? duration(task.durationMs) : "",
      task.tokens ? t("web.task.tokens", { n: tokens(task.tokens) }) : "",
    ].filter(Boolean),
  );
</script>

<details class="task-line" class:failed>
  <summary>
    <span class="task-kind">{label}</span>
    <span class="task-name">{task.name || task.summary || task.id}</span>
    {#each facts as fact (fact)}<span class="task-fact">{fact}</span>{/each}
    {#if at}<span class="when" title={fullTime(at)}>{shortTime(at)}</span>{/if}
  </summary>
  <dl class="kv">
    {#if task.id}<dt>task</dt><dd>{task.id}</dd>{/if}
    {#if task.toolUseID}<dt>tool use</dt><dd>{task.toolUseID}</dd>{/if}
    {#if task.outputFile}<dt>output</dt><dd>{task.outputFile}</dd>{/if}
    {#if task.summary}<dt>summary</dt><dd>{task.summary}</dd>{/if}
  </dl>
</details>
