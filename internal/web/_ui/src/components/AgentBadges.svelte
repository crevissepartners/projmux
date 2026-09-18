<script lang="ts">
  // An Agent's chips: the same dot and activity a Slot header draws for it,
  // with its `role` label between them. The dot is the Slot's: the
  // provider's light while online, red when offline, hollow when it cannot be
  // told. What the Agent is doing is read by `lib/activity`, as a Slot reads
  // it, and only while the Agent is online: an offline Agent is doing
  // nothing now.
  import { agentActivityOf } from "../lib/activity";
  import { t } from "../lib/i18n.svelte";
  import { onlineOf, type AgentRecord } from "../lib/tree";

  let { record }: { record: AgentRecord | null } = $props();

  const online = $derived(onlineOf(record));
  const activity = $derived(online === "online" ? agentActivityOf(record) : null);
  const onlineText = $derived(
    online === "online"
      ? t("web.graph.online")
      : online === "offline"
        ? record?.status === "missing-root"
          ? t("web.graph.missing_root_title")
          : t("web.graph.offline")
        : record
          ? t("web.graph.unknown_title")
          : t("web.graph.not_in_graph"),
  );
</script>

<span
  class="slot-dot"
  class:offline={online === "offline"}
  class:unknown={online === "unknown"}
  role="img"
  aria-label={onlineText}
  title={onlineText}
></span>
{#if record?.role}<span class="tag role">{record.role}</span>{/if}
<span class="slot-activity {activity?.tone || ''}">{activity ? t(`web.activity.${activity.kind}`) : ""}</span>
