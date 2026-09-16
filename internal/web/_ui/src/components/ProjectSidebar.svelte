<script lang="ts">
  import { byAttention } from "../lib/activity";
  import { t } from "../lib/i18n.svelte";
  import { go, route } from "../lib/router.svelte";
  import { live } from "../lib/state.svelte";
  import { livePanes, slotRef, type ProjectView } from "../lib/tree";
  import { listKeys, resizable } from "../lib/actions";

  interface Props {
    list?: HTMLElement;
    onEscape: () => void;
  }
  let { list = $bindable(), onEscape }: Props = $props();

  /**
   * Opening a Project lands where the work is: the pane that most needs
   * attention, else its first live window. A Project with nothing running
   * shows its overview; starting a session is left to the explicit ＋.
   */
  function open(project: ProjectView) {
    const ranked = livePanes(live.tree, project).sort(byAttention);
    if (ranked.length) {
      go({ project: project.uid, window: ranked[0].win.uid, pane: slotRef(ranked[0].pane) });
      return;
    }
    const first = project.windows.find((w) => !w.unbound && w.runtimeId);
    go({ project: project.uid, window: first?.uid ?? null });
  }
</script>

<aside class="sidebar" aria-label="Project" use:resizable={{ key: "sidebarWidth", edge: "right" }}>
  <h2>
    <span>Project</span>
    <span class="grow"></span>
    <kbd>Alt-1</kbd>
  </h2>
  <ul class="list" bind:this={list} use:listKeys={onEscape}>
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
