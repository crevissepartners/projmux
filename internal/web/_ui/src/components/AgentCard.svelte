<script lang="ts">
  // One Agent as a card: its name, its chips, its provider and phase. The
  // graph tab places these on its canvas and Home lists them by Project, so
  // an Agent reads the same wherever it is drawn. Where the card sits, and
  // what a click does, is the caller's: it passes them as it would to a
  // button.
  import type { HTMLButtonAttributes } from "svelte/elements";
  import { phaseText, providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { agentTitle, onlineOf, type AgentRecord } from "../lib/tree";
  import AgentBadges from "./AgentBadges.svelte";

  interface Props extends HTMLButtonAttributes {
    uid: string;
    record: AgentRecord | null;
  }
  let { uid, record, class: extra = "", ...rest }: Props = $props();

  const title = $derived(agentTitle(record, uid));
</script>

<button
  {...rest}
  type="button"
  class={["agent-card", extra]}
  data-provider={record?.provider}
  data-online={onlineOf(record)}
  title={t("web.graph.open_agent", { name: title.name })}
>
  <span class="agent-card-name" class:dim={title.dim}>{title.name}</span>
  <span class="agent-card-badges"><AgentBadges {record} /></span>
  {#if record}
    <span class="agent-card-meta"
      >{[providerText(record.provider), record.phase ? phaseText(record.phase) : ""].filter(Boolean).join(" · ")}</span
    >
  {/if}
</button>
