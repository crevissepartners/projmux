<script lang="ts">
  import { t } from "../lib/i18n.svelte";
  import { go, route } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import type { ProjectView } from "../lib/tree";
  import { listKeys, resizable } from "../lib/actions";

  interface Props {
    list?: HTMLElement;
    onEscape: () => void;
  }
  let { list = $bindable(), onEscape }: Props = $props();

  /**
   * Opening a Project lands on its first tab, the Agent graph: which Agents
   * it has and who talked to whom. A Window is one tab away.
   */
  function open(project: ProjectView) {
    go({ project: project.uid });
  }
</script>

<aside class="sidebar" aria-label="Project" use:resizable={{ key: "sidebarWidth", edge: "right" }}>
  <h2>
    <span>Project</span>
    <span class="grow"></span>
    <kbd>Alt-1</kbd>
  </h2>
  <ul class="list" bind:this={list} use:listKeys={onEscape}>
    <!-- Home is `/`: every Project's Agents, before any one Project. -->
    <li class="home-item">
      <button
        type="button"
        class="row"
        aria-current={route.sel.project || route.short ? undefined : "true"}
        onclick={() => go({})}
      >
        <div class="title"><span class="name">{t("web.home.title")}</span></div>
        <div class="sub">{t("web.home.sub")}</div>
      </button>
    </li>
    {#each live.tree.projects as project (project.uid)}
      <li>
        <button
          type="button"
          class="row"
          aria-current={route.sel.project === project.uid ? "true" : undefined}
          onclick={() => open(project)}
        >
          <div class="title">
            <span class="name">{project.name}</span>
            <span class="tags">
              {#if project.sessionLive}<span class="tag live">live</span>{/if}
            </span>
          </div>
          {#if project.root}<div class="sub" title={project.root}>{project.root}</div>{/if}
        </button>
      </li>
    {:else}
      <li class="empty">{t("web.projects.empty")}</li>
    {/each}
  </ul>
</aside>
