<script lang="ts">
  // Alt-5: the Settings a web page uses, read and saved through the same
  // functions as the terminal Settings, so both show the same values. Only
  // settings this page consumes are here; the rest stay in the terminal.
  import { get, patch, paths } from "../lib/api";
  import { explain, providerText } from "../lib/errors";
  import { loadMessages, t } from "../lib/i18n.svelte";
  import { fail } from "../lib/toast.svelte";
  import { ui } from "../lib/ui.svelte";
  import Picker from "./Picker.svelte";

  interface Choice {
    value: string;
    enabled: boolean;
  }
  interface UsageProvider {
    id: string;
    name: string;
    visible: boolean;
    windows: { key: string; label: string; visible: boolean }[];
  }
  interface SettingsBody {
    ai: { defaultMode: string; modes: Choice[]; providers: Choice[]; splitCwdFrom: string; splitCwdOrigin: string };
    statusbar: {
      notifications: boolean;
      usage: boolean;
      project: boolean;
      workingDirectory: boolean;
      git: boolean;
      resources: boolean;
      resourcesSupported: boolean;
      clock: boolean;
      usageProviders: UsageProvider[];
    };
    locale: { value: string; choices: string[] };
  }

  let { onClose }: { onClose: () => void } = $props();

  let settings = $state<SettingsBody | null>(null);
  let saving = $state("");
  let error = $state<{ text: string; detail: string } | null>(null);

  get<SettingsBody>(paths.settings)
    .then((body) => (settings = body))
    .catch((err) => (error = explain(err)));

  async function change(key: string, value: string) {
    saving = key;
    error = null;
    try {
      settings = await patch<SettingsBody>(paths.settings, { key, value });
    } catch (err) {
      fail(err);
      // A status bar save can land in the file and still fail to reload the
      // running tmux; show what is saved now either way.
      settings = await get<SettingsBody>(paths.settings).catch(() => settings);
    } finally {
      saving = "";
      if (key === "locale") await loadMessages();
      if (key.startsWith("statusbar.")) ui.settingsVersion++;
      if (key.startsWith("ai.")) ui.settingsVersion++;
    }
  }

  const onOff = (on: boolean) => (on ? "off" : "on");
  const modeText = (mode: string) =>
    mode === "selective"
      ? t("web.settings.mode_selective")
      : mode === "resume"
        ? t("web.settings.mode_resume")
        : mode === "shell"
          ? t("web.settings.mode_shell")
          : providerText(mode);
  const localeText = (value: string) => (value === "auto" ? t("web.settings.locale_auto") : value);
</script>

{#snippet toggle(key: string, label: string, on: boolean, disabled = false)}
  <button
    type="button"
    class="set-toggle"
    role="switch"
    aria-checked={on}
    disabled={disabled || !!saving}
    class:saving={saving === key}
    onclick={() => change(key, onOff(on))}
  >
    <span class="knob"></span>{label}
  </button>
{/snippet}

<Picker title={t("web.settings.title")} hint="Alt-5" wide {onClose}>
  <div class="settings">
    {#if error}
      <div class="notice err">{error.text}</div>
    {:else if !settings}
      <div class="empty">{t("web.status.loading")}</div>
    {:else}
      <section>
        <h4>{t("web.settings.ai")}</h4>
        <div class="set-row">
          <span class="set-label">{t("web.settings.default_mode")}</span>
          <div class="picker-kinds">
            {#each settings.ai.modes as mode (mode.value)}
              <button
                type="button"
                class="set-chip"
                aria-pressed={settings.ai.defaultMode === mode.value}
                disabled={!mode.enabled || !!saving}
                onclick={() => change("ai.defaultMode", mode.value)}>{modeText(mode.value)}</button
              >
            {/each}
          </div>
        </div>
        <div class="set-row">
          <span class="set-label">{t("web.settings.providers")}</span>
          <div class="set-group">
            {#each settings.ai.providers as provider (provider.value)}
              {@render toggle(`ai.provider.${provider.value}`, providerText(provider.value), provider.enabled)}
            {/each}
          </div>
        </div>
        <div class="set-row">
          <span class="set-label">{t("web.settings.split_cwd")}</span>
          <div class="picker-kinds">
            {#each ["project", "pane"] as source (source)}
              <button
                type="button"
                class="set-chip"
                aria-pressed={settings.ai.splitCwdFrom === source}
                disabled={!!saving}
                onclick={() => change("ai.splitCwdFrom", source)}>{t(`web.settings.split_${source}`)}</button
              >
            {/each}
          </div>
        </div>
      </section>

      <section>
        <h4>{t("web.settings.statusbar")}</h4>
        <div class="set-row">
          <span class="set-label">{t("web.settings.row0")}</span>
          <div class="set-group">
            {@render toggle("statusbar.notifications", t("web.settings.notifications"), settings.statusbar.notifications)}
            {@render toggle("statusbar.usage", t("web.settings.usage"), settings.statusbar.usage)}
          </div>
        </div>
        {#each settings.statusbar.usageProviders as provider (provider.id)}
          <div class="set-row sub">
            <span class="set-label">{provider.name}</span>
            <div class="set-group">
              {@render toggle(`statusbar.usage.${provider.id}`, t("web.settings.visible"), provider.visible, !settings.statusbar.usage)}
              {#each provider.windows as window (window.key)}
                {@render toggle(
                  `statusbar.usage.${provider.id}.${window.key}`,
                  window.label,
                  window.visible,
                  !settings.statusbar.usage || !provider.visible,
                )}
              {/each}
            </div>
          </div>
        {/each}
        <div class="set-row">
          <span class="set-label">{t("web.settings.row1")}</span>
          <div class="set-group">
            {@render toggle("statusbar.project", t("web.settings.project"), settings.statusbar.project)}
            {@render toggle("statusbar.working-directory", t("web.settings.cwd"), settings.statusbar.workingDirectory)}
            {@render toggle("statusbar.git", "Git", settings.statusbar.git)}
            {#if settings.statusbar.resourcesSupported}
              {@render toggle("statusbar.resources", t("web.settings.resources"), settings.statusbar.resources)}
            {/if}
            {@render toggle("statusbar.clock", t("web.settings.clock"), settings.statusbar.clock)}
          </div>
        </div>
      </section>

      <section>
        <h4>{t("web.settings.language")}</h4>
        <div class="set-row">
          <span class="set-label">{t("web.settings.language")}</span>
          <div class="picker-kinds">
            {#each settings.locale.choices as choice (choice)}
              <button
                type="button"
                class="set-chip"
                aria-pressed={settings.locale.value === choice}
                disabled={!!saving}
                onclick={() => change("locale", choice)}>{localeText(choice)}</button
              >
            {/each}
          </div>
        </div>
      </section>
      <p class="set-note">{t("web.settings.note")}</p>
    {/if}
  </div>
</Picker>
