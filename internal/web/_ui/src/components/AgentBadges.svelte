<script lang="ts">
  // An Agent's chips: its `role` label, whether it is online, and what its
  // provider hooks last said it was doing. A work state is only current while
  // the Agent is online; otherwise it is drawn faded as the last report.
  import { t } from "../lib/i18n.svelte";
  import { onlineOf, type AgentRecord } from "../lib/tree";

  let { record }: { record: AgentRecord | null } = $props();

  const tones: Record<string, string> = {
    idle: "idle",
    in_progress: "busy",
    input_required: "wait",
    approval_required: "alert",
    response_complete: "done",
  };

  const online = $derived(onlineOf(record));
  const work = $derived(record && tones[record.activity] ? record.activity : "");
</script>

{#if record?.role}<span class="tag role">{record.role}</span>{/if}
{#if online === "online"}
  <span class="tag live">{t("web.graph.online")}</span>
{:else if online === "offline"}
  <span class="tag gone">{t("web.graph.offline")}</span>
  {#if record?.status === "missing-root"}
    <span class="tag gone soft" title={t("web.graph.missing_root_title")}>{t("web.graph.missing_root")}</span>
  {/if}
{:else}
  <span class="tag unknown" title={record ? t("web.graph.unknown_title") : t("web.graph.not_in_graph")}
    >{t("web.graph.unknown")}</span
  >
{/if}
{#if work}
  <span
    class="tag work w-{tones[work]}"
    class:stale={online !== "online"}
    title={online !== "online" ? t("web.graph.stale_work") : undefined}>{t(`web.activity.${work}`)}</span
  >
{/if}
