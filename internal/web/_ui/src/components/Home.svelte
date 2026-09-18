<script lang="ts">
  // Home: every Agent the Registry has, of every Project, offline ones too,
  // drawn as the same cards as a Project's graph tab and grouped by Project
  // in the sidebar's order. There is no graph here, only who exists and in
  // what state. A card opens the Agent layer over the page; the address and
  // the Window stay as they are.
  import { t } from "../lib/i18n.svelte";
  import { live } from "../lib/state.svelte";
  import { agentTitle, onlineOf, type AgentRecord } from "../lib/tree";
  import type { LayerTarget } from "../lib/types";
  import AgentCard from "./AgentCard.svelte";
  import AgentLayer from "./AgentLayer.svelte";

  interface Group {
    /** The Project's uid; "" for Agents whose Window has no Project. */
    uid: string;
    name: string;
    agents: AgentRecord[];
  }

  const lower = (text: string) => text.toLowerCase();

  // Agents by the name a card shows, then by uid, so equal names keep a
  // fixed order between reads.
  function byName(a: AgentRecord, b: AgentRecord): number {
    return lower(agentTitle(a, a.uid).name).localeCompare(lower(agentTitle(b, b.uid).name)) || a.uid.localeCompare(b.uid);
  }

  // Projects in the sidebar's order. A Project uid the tree does not have is
  // named by its uid after them, and Agents with no Project come last.
  const groups = $derived.by((): Group[] => {
    const byProject = new Map<string, AgentRecord[]>();
    for (const agent of live.tree.agents) {
      const list = byProject.get(agent.projectUID) || [];
      list.push(agent);
      byProject.set(agent.projectUID, list);
    }
    const out: Group[] = [];
    for (const project of live.tree.projects) {
      const agents = byProject.get(project.uid);
      if (!agents) continue;
      byProject.delete(project.uid);
      out.push({ uid: project.uid, name: project.name, agents });
    }
    const loose = byProject.get("");
    byProject.delete("");
    for (const uid of [...byProject.keys()].sort()) out.push({ uid, name: uid, agents: byProject.get(uid)! });
    if (loose) out.push({ uid: "", name: t("web.graph.no_project"), agents: loose });
    for (const group of out) group.agents.sort(byName);
    return out;
  });

  const online = $derived(live.tree.agents.filter((a) => onlineOf(a) === "online").length);
  const offline = $derived(live.tree.agents.filter((a) => onlineOf(a) === "offline").length);
  const projects = $derived(groups.filter((g) => g.uid).length);

  let layer = $state<LayerTarget | null>(null);
</script>

<div class="home">
  <div class="home-head">
    <h3>{t("web.home.title")}</h3>
    <span class="home-sum">
      {t("web.home.summary", { agents: live.tree.agents.length, projects, online, offline })}
    </span>
  </div>
  {#if !live.tree.agents.length}
    <p class="empty">{live.updatedAt ? t("web.home.empty") : t("web.status.loading")}</p>
  {:else}
    {#each groups as group (group.uid)}
      <section class="home-group">
        <h4 class="home-group-head" class:dim={!group.uid}>
          <span class="home-group-name" title={group.uid || undefined}>{group.name}</span>
          <span class="home-group-count">{group.agents.length}</span>
        </h4>
        <div class="home-cards">
          {#each group.agents as agent (agent.uid)}
            <AgentCard uid={agent.uid} record={agent} onclick={() => (layer = { kind: "agent", uid: agent.uid })} />
          {/each}
        </div>
      </section>
    {/each}
  {/if}
</div>

{#if layer}
  <AgentLayer target={layer} onClose={() => (layer = null)} />
{/if}
