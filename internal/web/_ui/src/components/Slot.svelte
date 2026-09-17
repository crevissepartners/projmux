<script lang="ts">
  // One pane on screen: its header, and its conversation or its record.
  import { activityOf } from "../lib/activity";
  import { closePane, renamePane } from "../lib/commands";
  import { phaseText, providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { go, route, shortPath } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { paneLabel, slotRef, type PaneView, type ProjectView, type WindowView } from "../lib/tree";
  import Chat from "./Chat.svelte";
  import CopyButton from "./CopyButton.svelte";
  import InlineName from "./InlineName.svelte";
  import OpenInTerminal from "./OpenInTerminal.svelte";
  import Popover from "./Popover.svelte";

  interface Props {
    project: ProjectView;
    win: WindowView;
    pane: PaneView;
    index: number;
    focused: boolean;
    style: string;
  }
  let { project, win, pane, index, focused, style }: Props = $props();

  // Each slot is tinted by its provider, so two conversations side by side
  // are told apart before either is read.
  const provider = $derived(pane.agent?.provider || "shell");
  const activity = $derived(activityOf(pane));
  const label = $derived(paneLabel(pane));
  let stream = $state<"" | "live" | "warn">("");
  let model = $state<{ model: string; effort: string } | null>(null);
  // "claude-opus-5" reads as "opus-5" beside a Claude chip.
  $effect(() => {
    void chatKey;
    model = null;
  });
  const modelText = $derived(
    model ? `${model.model.replace(/^claude-/, "")}${model.effort ? ` · ${model.effort}` : ""}` : "",
  );

  // A rebuild would drop an open composer and the log's scroll, so the chat is
  // keyed only on what changes what it shows: which agent, its phase, its pane.
  const chatKey = $derived(`${pane.agent?.uid}:${pane.agent?.phase}:${pane.runtimeId}`);

  function focus() {
    if (route.sel.pane !== slotRef(pane)) go({ project: project.uid, window: win.uid, pane: slotRef(pane) });
  }

</script>

<!-- The whole slot takes focus: clicking or tabbing into it makes it the
     focused pane, which is what the URL tracks. -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<section
  class="slot"
  class:focused
  class:shell={!pane.agent}
  data-provider={provider}
  data-activity={activity?.tone || ""}
  data-pane={pane.uid}
  {style}
  onmousedown={focus}
  onfocusin={focus}
  role="group"
>
  <div class="slot-head">
    <span class="slot-dot"></span>
    <span class="slot-num">#{index}</span>
    <InlineName
      className="slot-title"
      value={label.name}
      dim={label.dim}
      title={t("web.rename.hint")}
      rename={(name) => renamePane(project.uid, win.uid, pane.uid, pane.agent?.uid ?? null, name)}
    />
    <span class="slot-kind">
      {providerText(provider)}{pane.agent && pane.agent.phase !== "Running" ? ` · ${phaseText(pane.agent.phase)}` : ""}
    </span>
    {#if modelText}<span class="slot-model" title="{model?.model}{model?.effort ? ` · effort ${model.effort}` : ''}">{modelText}</span>{/if}
    <span class="slot-activity {activity?.tone || ''}">{activity ? t(`web.activity.${activity.kind}`) : ""}</span>
    {#if stream}
      <span class="tag stream" class:live={stream === "live"} class:warn={stream === "warn"} title={t("web.slot.live_title")}>
        {stream === "live" ? "live" : t("web.status.reconnecting")}
      </span>
    {/if}
    <span class="grow"></span>
    <!-- A slot waiting on input or approval is waiting on something the web
         cannot answer yet; the terminal is one click away. -->
    {#if activity?.tone === "wait" || activity?.tone === "alert"}
      <OpenInTerminal paneUID={pane.uid} compact />
    {/if}
    <Popover face="ⓘ" title={t("web.slot.record")}>{@render record()}</Popover>
    <button
      type="button"
      class="slot-btn"
      title={t("web.slot.close")}
      onmousedown={(e) => e.stopPropagation()}
      onclick={async (e) => {
        e.stopPropagation();
        if ((await closePane(project.uid, win.uid, pane.uid)) && route.sel.pane === slotRef(pane)) {
          go({ project: project.uid, window: win.uid });
        }
      }}>×</button
    >
  </div>

  {#snippet record()}
    <div class="slot-info">
      <dl class="kv">
        <dt>pane</dt>
        <dd>{pane.uid}</dd>
        <dt>runtime</dt>
        <dd>{pane.runtimeId || t("web.slot.none")}</dd>
        {#if pane.cwd}<dt>cwd</dt><dd>{pane.cwd}</dd>{/if}
        {#if pane.agent}
          <dt>agent</dt>
          <dd>{pane.agent.uid}</dd>
          <dt>link</dt>
          <dd class="with-copy">
            <span>{shortPath(pane.agent.uid)}</span><CopyButton text={location.origin + shortPath(pane.agent.uid)} />
          </dd>
          <dt>provider</dt>
          <dd>{pane.agent.provider}</dd>
          <dt>phase</dt>
          <dd>{pane.agent.phase}{pane.agent.reason ? ` (${pane.agent.reason})` : ""}</dd>
          {#if model}
            <dt>model</dt>
            <dd>{model.model}</dd>
            {#if model.effort}<dt>effort</dt><dd>{model.effort}</dd>{/if}
          {/if}
        {/if}
      </dl>
    </div>
  {/snippet}

  {#if pane.agent}
    <!-- Two panes side by side read as two chat windows: the conversation
         and its composer fill the slot, and the details open from the head. -->
    <div class="slot-body chatting">
      {#key chatKey}
        <Chat agent={pane.agent} paneUID={pane.uid} bind:stream bind:model />
      {/key}
    </div>
  {:else}
    <div class="slot-body">{@render record()}</div>
  {/if}
</section>
