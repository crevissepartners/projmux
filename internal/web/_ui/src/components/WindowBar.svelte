<script lang="ts">
  import { activityOf, needsYou } from "../lib/activity";
  import { closeWindow, createWindow, renameWindow } from "../lib/commands";
  import { launchDefault } from "../lib/launch.svelte";
  import { t } from "../lib/i18n.svelte";
  import { go, route } from "../lib/router.svelte";
  import { slotRef, type ProjectView, type WindowView } from "../lib/tree";
  import { setToggle, ui } from "../lib/ui.svelte";
  import InlineName from "./InlineName.svelte";

  interface Props {
    project: ProjectView | null;
    current: WindowView | null;
    onDesktopNotify: () => void;
  }
  let { project, current, onDesktopNotify }: Props = $props();

  const waiting = (win: WindowView) => win.panes.some((p) => needsYou(activityOf(p)?.tone));
  const busy = (win: WindowView) => win.panes.some((p) => activityOf(p)?.tone === "busy");

  /** The first pane worth landing on, so changing Window lands on a Pane. */
  function landing(win: WindowView) {
    const first = win.panes.find((p) => p.runtimeId && (ui.showShell || p.agent));
    return first ? slotRef(first) : null;
  }

  const shells = $derived((current?.panes || []).filter((p) => p.runtimeId && !p.agent).length);
</script>

<div class="winbar">
  <div class="tabbar" role="tablist" aria-label="Window">
    {#if !project}
      <span class="empty">{t("web.windows.pick_project")}</span>
    {:else}
      <!-- The Project's own tab comes first and cannot be closed: its Agents
           and who talked to whom. -->
      <div
        class="tab graph-tab"
        role="tab"
        tabindex="0"
        aria-selected={route.sel.project === project.uid && !route.sel.window}
        title={t("web.graph.tab_title")}
        onclick={() => go({ project: project.uid })}
        onkeydown={(e) => e.key === "Enter" && go({ project: project.uid })}
      >
        <span class="graph-tab-mark" aria-hidden="true">◇</span>
        <span>{t("web.graph.tab")}</span>
      </div>
      {#each project.windows as win, index (win.uid)}
        {@const named = !!win.name && win.name !== win.uid}
        <div
          class="tab"
          role="tab"
          tabindex="0"
          aria-selected={current?.uid === win.uid}
          title="pane {win.panes.length}"
          onclick={() => go({ project: project.uid, window: win.uid, pane: landing(win) })}
          onkeydown={(e) => e.key === "Enter" && go({ project: project.uid, window: win.uid, pane: landing(win) })}
        >
          {#if waiting(win)}
            <span class="tag attn" title={t("web.windows.needs_you")}>●</span>
          {:else if busy(win)}
            <span class="tag busy" title={t("web.windows.busy")}></span>
          {/if}
          <span class="idx">{index}:</span>
          <!-- An unnamed Window is its uid, which is not a label. -->
          <InlineName
            value={named ? win.name : t("web.window.untitled", { n: index })}
            dim={!named}
            title={t("web.rename.hint")}
            rename={(name) => renameWindow(project.uid, win.uid, name)}
          />
          {#if win.unbound || !win.runtimeId}
            <span class="tag warn" title={win.unboundReason || t("web.windows.unbound_title")}>{t("web.window.not_running")}</span>
          {/if}
          <!-- One click closes, like a Pane's ×, unless the Window holds a
               Running Agent; then one confirmation names it first. -->
          <button
            type="button"
            class="tab-close"
            title={t("web.windows.close")}
            onclick={async (e) => {
              e.stopPropagation();
              if ((await closeWindow(project.uid, win.uid)) && current?.uid === win.uid) go({ project: project.uid });
            }}>×</button
          >
        </div>
      {:else}
        {#if ui.creatingWindow !== project.uid}<span class="empty">{t("web.windows.empty")}</span>{/if}
      {/each}
      {#if ui.creatingWindow === project.uid}
        <div class="tab creating" role="status"><span class="tag busy"></span>{t("web.windows.creating")}</div>
      {/if}
      <button
        type="button"
        class="tab add"
        title={t("web.windows.new")}
        disabled={!!ui.creatingWindow}
        onclick={() => createWindow(project.uid)}>＋</button
      >
      {#if current}
        <button type="button" class="tab add" title={t("web.windows.split")} onclick={() => launchDefault()}
          >⊞</button
        >
      {/if}
    {/if}
  </div>
  {#if project}
    <div class="tools">
      {#if current?.runtimeId}
        <button
          type="button"
          class="tbtn"
          aria-pressed={ui.layout}
          title={t("web.tools.layout_title")}
          onclick={() => setToggle("layout", !ui.layout)}>{t("web.tools.layout")}</button
        >
      {/if}
      {#if shells || ui.showShell}
        <button
          type="button"
          class="tbtn"
          aria-pressed={ui.showShell}
          title={t("web.tools.shells_title")}
          onclick={() => setToggle("showShell", !ui.showShell)}>{ui.showShell ? `sh ${shells}` : `sh +${shells}`}</button
        >
      {/if}
      <button
        type="button"
        class="tbtn"
        aria-pressed={ui.desktopNotify}
        title={t("web.tools.notify_title")}
        onclick={onDesktopNotify}>{t("web.tools.notify")}</button
      >
      <button type="button" class="tbtn" title={t("web.tools.settings_title")} onclick={() => (ui.overlay = "settings")}
        >{t("web.tools.settings")}</button
      >
      <button type="button" class="tbtn" title={t("web.tools.help")} onclick={() => (ui.overlay = "help")}>?</button>
    </div>
  {/if}
</div>
