<script lang="ts">
  // A Project's first tab: its Agents as cards, and a line between two that
  // sent each other messages. Agents of other Projects that talked with this
  // one sit in a box per Project. A card opens the Agent's transcript, a line
  // the pair's messages, both in the one Agent layer.
  //
  // Which Agents talked comes from the agent-graph read; what a card says
  // about an Agent (name, role, online, work) comes from the live graph the
  // page already follows, joined by uid.
  import { onDestroy } from "svelte";
  import { get, paths } from "../lib/api";
  import { layoutAgentGraph } from "../lib/agentGraphLayout";
  import { explain, phaseText, providerText } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { live } from "../lib/state.svelte";
  import { fullTime, shortTime } from "../lib/time";
  import { agentTitle, onlineOf, type AgentRecord, type ProjectView } from "../lib/tree";
  import type { AgentGraph, AgentGraphEdge, LayerTarget } from "../lib/types";
  import AgentBadges from "./AgentBadges.svelte";
  import AgentLayer from "./AgentLayer.svelte";

  let { projectUID, project }: { projectUID: string; project: ProjectView | null } = $props();

  let data = $state<AgentGraph | null>(null);
  let error = $state<{ text: string; detail: string } | null>(null);
  let loading = $state(false);
  let reading: AbortController | null = null;

  async function load(uid: string) {
    reading?.abort();
    const mine = new AbortController();
    reading = mine;
    loading = true;
    try {
      const body = await get<AgentGraph>(paths.agentGraph(uid), mine.signal);
      if (reading !== mine) return;
      data = body;
      error = null;
    } catch (err) {
      if (reading !== mine) return;
      data = null;
      error = explain(err);
    }
    loading = false;
  }

  $effect(() => {
    data = null;
    error = null;
    load(projectUID);
  });
  onDestroy(() => reading?.abort());

  const records = $derived(new Map(live.tree.agents.map((a) => [a.uid, a])));
  const record = (uid: string): AgentRecord | null => records.get(uid) || null;
  const nameOf = (uid: string) => agentTitle(record(uid), uid).name;

  const layout = $derived(data ? layoutAgentGraph(data) : null);
  const edgeOf = $derived(new Map((data?.edges || []).map((e) => [`${e.a} ${e.b}`, e])));

  function areaName(uid: string, home: boolean): string {
    if (home) return project?.name || uid;
    if (!uid) return t("web.graph.no_project");
    return live.tree.projects.find((p) => p.uid === uid)?.name || uid;
  }

  function edgeTitle(edge: AgentGraphEdge): string {
    return t("web.graph.edge_title", {
      a: nameOf(edge.a),
      b: nameOf(edge.b),
      ab: edge.aToB,
      ba: edge.bToA,
      time: fullTime(edge.lastAcceptedAt),
    });
  }

  let hovered = $state("");
  let layer = $state<LayerTarget | null>(null);

  function openPair(edge: AgentGraphEdge) {
    layer = { kind: "pair", a: edge.a, b: edge.b, edge };
  }

  function pairKey(event: KeyboardEvent, edge: AgentGraphEdge) {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    openPair(edge);
  }
</script>

<div class="graph-view">
  {#if error}
    <div class="graph-banner">
      <div class="notice err">
        {t("web.graph.load_failed")}
        {error.text}
        <details class="toast-detail"><summary>{t("web.error.details")}</summary><pre>{error.detail}</pre></details>
      </div>
    </div>
  {:else if !data || !layout}
    <p class="empty">{t("web.status.loading")}</p>
  {:else if !data.agents.length}
    <p class="empty">{t("web.graph.empty")}</p>
  {:else}
    <div class="graph-scroll">
      <div class="graph-canvas" style:width="{layout.width}px" style:height="{layout.height}px">
        {#each layout.areas as area (area.projectUID)}
          <div
            class="graph-area"
            class:home={area.home}
            style:left="{area.x}px"
            style:top="{area.y}px"
            style:width="{area.w}px"
            style:height="{area.h}px"
          >
            <span class="graph-area-label" title={area.projectUID}>{areaName(area.projectUID, area.home)}</span>
          </div>
        {/each}
        <svg class="graph-edges" width={layout.width} height={layout.height}>
          {#each layout.edges as path (`${path.a} ${path.b}`)}
            {@const edge = edgeOf.get(`${path.a} ${path.b}`)}
            {#if edge}
              <!-- The wide transparent stroke is what takes the click; the
                   thin one is what is seen. -->
              <g
                class="graph-edge"
                class:lit={hovered === edge.a || hovered === edge.b}
                role="button"
                tabindex="0"
                aria-label={edgeTitle(edge)}
                onclick={() => openPair(edge)}
                onkeydown={(e) => pairKey(e, edge)}
              >
                <title>{edgeTitle(edge)}</title>
                <path class="hit" d={path.d} />
                <path class="line" d={path.d} />
              </g>
            {/if}
          {/each}
        </svg>
        {#each layout.cards as card (card.uid)}
          {@const rec = record(card.uid)}
          {@const title = agentTitle(rec, card.uid)}
          <button
            type="button"
            class="graph-card"
            data-provider={rec?.provider}
            data-online={onlineOf(rec)}
            style:left="{card.x}px"
            style:top="{card.y}px"
            style:width="{card.w}px"
            style:height="{card.h}px"
            title={t("web.graph.open_agent", { name: title.name })}
            onclick={() => (layer = { kind: "agent", uid: card.uid })}
            onmouseenter={() => (hovered = card.uid)}
            onmouseleave={() => hovered === card.uid && (hovered = "")}
            onfocus={() => (hovered = card.uid)}
            onblur={() => hovered === card.uid && (hovered = "")}
          >
            <span class="graph-card-name" class:dim={title.dim}>{title.name}</span>
            <span class="graph-card-badges"><AgentBadges record={rec} /></span>
            {#if rec}
              <span class="graph-card-meta"
                >{[providerText(rec.provider), rec.phase ? phaseText(rec.phase) : ""].filter(Boolean).join(" · ")}</span
              >
            {/if}
          </button>
        {/each}
      </div>
    </div>
  {/if}
  <div class="graph-foot">
    {#if data}
      <span title={data.since ? fullTime(data.since) : undefined}>
        {data.since ? t("web.graph.since", { time: shortTime(data.since) }) : t("web.graph.since_none")}
      </span>
      <span>{t("web.graph.omitted", { pairs: data.omitted.pairs, messages: data.omitted.messages })}</span>
      {#if data.agents.length && !data.edges.length}<span>{t("web.graph.no_edges")}</span>{/if}
      {#if data.skipped}<span class="warn">{t("web.graph.skipped", { n: data.skipped })}</span>{/if}
    {/if}
    <span class="grow"></span>
    <button type="button" class="tbtn" disabled={loading} onclick={() => load(projectUID)}>{t("web.graph.reload")}</button>
  </div>
</div>

{#if layer}
  <AgentLayer target={layer} onClose={() => (layer = null)} />
{/if}
