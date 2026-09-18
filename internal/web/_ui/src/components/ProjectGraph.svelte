<script lang="ts">
  // A Project's first tab: its Agents as cards, laid out by who created whom
  // from left to right, a line between two that sent each other messages,
  // and an arrow from an Agent to each Agent it created. Agents of other
  // Projects with an edge to this one sit in a box per Project, below. A card
  // opens the Agent's transcript, a conversation line the pair's messages,
  // both in the one Agent layer; an arrow only says who created whom.
  //
  // Which Agents talked and created comes from the agent-graph read; what a
  // card says about an Agent (name, role, online, work) comes from the live
  // graph the page already follows, joined by uid.
  import { onDestroy } from "svelte";
  import { get, paths } from "../lib/api";
  import { layoutAgentGraph } from "../lib/agentGraphLayout";
  import { explain } from "../lib/errors";
  import { t } from "../lib/i18n.svelte";
  import { live } from "../lib/state.svelte";
  import { fullTime, shortTime } from "../lib/time";
  import { agentTitle, type AgentRecord, type ProjectView } from "../lib/tree";
  import type { AgentConversationEdge, AgentCreatedEdge, AgentGraph, LayerTarget } from "../lib/types";
  import AgentCard from "./AgentCard.svelte";
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

  // The role and name a card shows also place it: role picks the column, name
  // the order within one. An Agent the live graph lacks has no role (null).
  const layout = $derived(
    data
      ? layoutAgentGraph({
          project: data.project,
          agents: data.agents.map((a) => ({ ...a, role: record(a.uid)?.role ?? null, name: nameOf(a.uid) })),
          edges: data.edges,
        })
      : null,
  );
  // A pair can have both a conversation and a created edge, so edges are
  // looked up, and keyed, by kind as well as by pair.
  const edgeKey = (e: { kind: string; a: string; b: string }) => `${e.kind} ${e.a} ${e.b}`;
  const conversationOf = $derived(
    new Map(
      (data?.edges || [])
        .filter((e): e is AgentConversationEdge => e.kind === "conversation")
        .map((e) => [edgeKey(e), e]),
    ),
  );
  const createdOf = $derived(
    new Map(
      (data?.edges || []).filter((e): e is AgentCreatedEdge => e.kind === "created").map((e) => [edgeKey(e), e]),
    ),
  );

  function areaName(uid: string, home: boolean): string {
    if (home) return project?.name || uid;
    if (!uid) return t("web.graph.no_project");
    return live.tree.projects.find((p) => p.uid === uid)?.name || uid;
  }

  function edgeTitle(edge: AgentConversationEdge): string {
    return t("web.graph.edge_title", {
      a: nameOf(edge.a),
      b: nameOf(edge.b),
      ab: edge.aToB,
      ba: edge.bToA,
      time: fullTime(edge.lastAcceptedAt),
    });
  }

  function createdTitle(edge: AgentCreatedEdge): string {
    return t("web.graph.created_title", { a: nameOf(edge.a), b: nameOf(edge.b) });
  }

  let hovered = $state("");
  let layer = $state<LayerTarget | null>(null);

  function openPair(edge: AgentConversationEdge) {
    layer = { kind: "pair", a: edge.a, b: edge.b, edge };
  }

  function pairKey(event: KeyboardEvent, edge: AgentConversationEdge) {
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
          <defs>
            <!-- A marker's fill cannot follow the stroke of the path using
                 it, so a lit arrow uses its own head. -->
            <marker
              id="graph-arrow"
              class="graph-arrowhead"
              viewBox="0 0 10 10"
              refX="10"
              refY="5"
              markerWidth="9"
              markerHeight="9"
              markerUnits="userSpaceOnUse"
              orient="auto"
            >
              <path d="M0 0 L10 5 L0 10 z" />
            </marker>
            <marker
              id="graph-arrow-lit"
              class="graph-arrowhead lit"
              viewBox="0 0 10 10"
              refX="10"
              refY="5"
              markerWidth="10"
              markerHeight="10"
              markerUnits="userSpaceOnUse"
              orient="auto"
            >
              <path d="M0 0 L10 5 L0 10 z" />
            </marker>
          </defs>
          {#each layout.edges as path (edgeKey(path))}
            {@const created = path.kind === "created" ? createdOf.get(edgeKey(path)) : undefined}
            {@const edge = path.kind === "conversation" ? conversationOf.get(edgeKey(path)) : undefined}
            {#if created}
              {@const lit = hovered === created.a || hovered === created.b}
              <!-- Who created whom: shown and titled, never a button. -->
              <g class="graph-created" class:lit>
                <title>{createdTitle(created)}</title>
                <path class="hit" d={path.d} />
                <path class="line" d={path.d} marker-end={lit ? "url(#graph-arrow-lit)" : "url(#graph-arrow)"} />
              </g>
            {:else if edge}
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
          <AgentCard
            uid={card.uid}
            record={record(card.uid)}
            style="left: {card.x}px; top: {card.y}px; width: {card.w}px; height: {card.h}px"
            onclick={() => (layer = { kind: "agent", uid: card.uid })}
            onmouseenter={() => (hovered = card.uid)}
            onmouseleave={() => hovered === card.uid && (hovered = "")}
            onfocus={() => (hovered = card.uid)}
            onblur={() => hovered === card.uid && (hovered = "")}
          />
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
      <span>{t("web.graph.omitted_created", { n: data.omitted.created ?? 0 })}</span>
      {#if data.agents.length && !conversationOf.size}<span>{t("web.graph.no_edges")}</span>{/if}
      {#if data.skipped}<span class="warn">{t("web.graph.skipped", { n: data.skipped })}</span>{/if}
    {/if}
    <span class="grow"></span>
    {#if data?.agents.length}
      <span class="graph-legend">
        <span class="graph-legend-item">
          <svg width="26" height="10" aria-hidden="true"><path class="conversation" d="M1 5 H25" /></svg>
          {t("web.graph.legend_conversation")}
        </span>
        <span class="graph-legend-item">
          <svg width="26" height="10" aria-hidden="true"
            ><path class="created" d="M1 5 H18" /><path class="created-head" d="M17 1 L25 5 L17 9 z" /></svg
          >
          {t("web.graph.legend_created")}
        </span>
      </span>
    {/if}
    <button type="button" class="tbtn" disabled={loading} onclick={() => load(projectUID)}>{t("web.graph.reload")}</button>
  </div>
</div>

{#if layer}
  <AgentLayer target={layer} onClose={() => (layer = null)} />
{/if}
