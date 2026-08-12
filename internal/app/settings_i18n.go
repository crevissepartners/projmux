package app

import (
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/i18n"
)

// uiTextKeys maps a user-facing English literal to its catalog key. It is the
// single shared registry behind both the Settings localization helpers and the
// common picker choke point (runPickerOptionBackend), so every picker title,
// prompt, footer, header, and static row label resolves through the catalog.
//
// Adding an entry here is necessary but not sufficient: the key must also exist
// in internal/i18n/default_catalog.go for both en-US (FallbackLocale) and
// ko-KR. The coverage tests enforce both halves.
var uiTextKeys = map[string]i18n.Key{
	"Settings": "settings.root.title",
	"Global":   "settings.root.tab.global",
	"Project":  "settings.root.tab.project",

	"Start project":                                                 "settings.title.start_project",
	"Project recipe - Startup command":                              "settings.title.project_recipe_startup",
	"Project recipe - Kube":                                         "settings.title.project_recipe_kube",
	"Project recipe - Environment":                                  "settings.title.project_recipe_environment",
	"Project recipe - env, kube, startup":                           "settings.title.project_recipe_overview",
	"Effective merge view - global + project config":                "settings.title.effective_merge",
	"Session State - Project auto-save":                             "settings.title.session_state_project_autosave",
	"Session State - Project snapshot actions":                      "settings.title.session_state_project_snapshot_actions",
	"Session State - Auto-save interval":                            "settings.title.session_state_autosave_interval",
	"Save named snapshot":                                           "settings.title.save_named_snapshot",
	"Delete session snapshot - confirm":                             "settings.title.delete_session_snapshot_confirm",
	"Trust - Project config hash":                                   "settings.title.trust_project_config_hash",
	"Untrust project config - confirm":                              "settings.title.untrust_project_config_confirm",
	"Projects > Sessions > State":                                   "settings.title.projects_sessions_state",
	"AI Settings - Default split mode":                              "settings.title.ai_default_split_mode",
	"AI Launch - Split direction":                                   "settings.title.ai_launch_split_direction",
	"Notifications - Delivery and desktop controls":                 "settings.title.notifications",
	"Hooks - Global lifecycle hook paths":                           "settings.title.hooks_global",
	"Hooks - Project lifecycle hook paths":                          "settings.title.hooks_project",
	"Project Picker - Project roots, workdirs, and pinned projects": "settings.title.project_picker",
	"Appearance - Icon decoration":                                  "settings.title.appearance",
	"Appearance - AI badge and icon decoration":                     "settings.title.appearance_badge_icon",
	"Appearance - Language, AI badge, status/notify icons":          "settings.title.appearance_badge_icon",
	"Session State - Restore and autosave controls":                 "settings.title.session_state",
	"Session State - Auto-save":                                     "settings.title.session_state_autosave",
	"Session State - Project settings unavailable":                  "settings.title.session_state_project_unavailable",
	"Labs - Experimental features":                                  "settings.title.labs",
	"About - Version, updates, key setup":                           "settings.title.about",
	"About - Version, updates, welcome, and quit":                   "settings.title.about",
	"AI Settings - Enabled agents":                                  "settings.title.ai_enabled_agents",
	"AI Settings - Resume picker":                                   "settings.title.ai_resume_picker",
	"AI Settings - Picker limit":                                    "settings.title.ai_resume_picker_limit",
	"AI Settings - Scan depth":                                      "settings.title.ai_resume_picker_depth",
	"AI Settings - Custom resume picker limit":                      "settings.title.ai_custom_resume_picker_limit",
	"AI Settings - Custom resume scan depth":                        "settings.title.ai_custom_resume_scan_depth",
	"Add Project - Choose a filesystem directory":                   "settings.title.add_project",
	"Project Root - Effective and saved root":                       "settings.title.project_root",
	"Set Project Root - Type one absolute primary root path":        "settings.title.set_project_root",
	"Add Workdir - Choose or type a directory to scan":              "settings.title.add_workdir",
	"Type Workdir - Absolute path":                                  "settings.title.type_workdir",
	"Workdirs - Saved and inherited scan roots":                     "settings.title.workdirs",
	"Pinned Projects - Add or remove pins":                          "settings.title.pinned_projects",
	"Notifications - AI dedupe window":                              "settings.title.notifications_ai_dedupe",
	"Notifications - Custom AI dedupe":                              "settings.title.notifications_custom_ai_dedupe",
	"Notifications - Desktop notifications":                         "settings.title.notifications_desktop",
	"Notifications - Hook quiet policy":                             "settings.title.notifications_hook_quiet_policy",
	"Notifications - Delivery sources":                              "settings.title.notifications_delivery_sources",
	"Hook quiet policy":                                             "settings.text.hook_quiet_policy",
	"Appearance - Path icon":                                        "settings.title.appearance_path_icon",
	"Appearance - Git icon":                                         "settings.title.appearance_git_icon",
	"Appearance - Notify icon":                                      "settings.title.appearance_notify_icon",
	"Appearance - AI badge style":                                   "settings.title.appearance_ai_badge_style",
	"Appearance - Language / Locale":                                "settings.title.appearance_language_locale",
	"Theme - Global values":                                         "settings.title.theme_global_values",
	"Theme - Preset selector":                                       "settings.title.theme_preset_selector",
	"Keybinding":                                                    "settings.text.keybinding",
	"Edit Keys":                                                     "settings.text.edit_keys",
	"Replace aliases":                                               "settings.text.replace_aliases",
	"Keybinding Lab - Diagnose delivery":                            "settings.title.keybinding_lab_diagnose_delivery",
	"Session State - Sidebar startup picker":                        "settings.title.session_state_sidebar_startup_picker",
	"Labs - Project Hooks":                                          "settings.title.labs_project_hooks",

	"Start project > ":                                         "settings.prompt.start_project",
	"Update > ":                                                "settings.prompt.update",
	"Settings > AI Settings > ":                                "settings.prompt.settings_ai",
	"Settings > Notifications > ":                              "settings.prompt.settings_notifications",
	"Settings > Hooks > ":                                      "settings.prompt.settings_hooks",
	"Settings > Project > Hooks > ":                            "settings.prompt.settings_project_hooks",
	"Settings > Project Picker > ":                             "settings.prompt.settings_project_picker",
	"Settings > Appearance > ":                                 "settings.prompt.settings_appearance",
	"Settings > Session State > ":                              "settings.prompt.settings_session_state",
	"Settings > Project > Session State > ":                    "settings.prompt.settings_project_session_state",
	"Settings > Labs > ":                                       "settings.prompt.settings_labs",
	"Settings > About > ":                                      "settings.prompt.settings_about",
	"Settings > Project Picker > Add Project > ":               "settings.prompt.settings_add_project",
	"Settings > Project Picker > Project Root > ":              "settings.prompt.settings_project_root",
	"Type project root path > ":                                "settings.prompt.type_project_root_path",
	"Settings > Project Picker > Add Workdir > ":               "settings.prompt.settings_add_workdir",
	"Type workdir path > ":                                     "settings.prompt.type_workdir_path",
	"Settings > Project Picker > Workdirs > ":                  "settings.prompt.settings_workdirs",
	"Settings > Project Picker > Pinned Projects > ":           "settings.prompt.settings_pinned_projects",
	"Settings > Notifications > AI dedupe > ":                  "settings.prompt.settings_ai_dedupe",
	"AI dedupe seconds > ":                                     "settings.prompt.ai_dedupe_seconds",
	"Settings > Notifications > Desktop notifications > ":      "settings.prompt.settings_desktop_notifications",
	"Settings > AI Settings > Default split mode > ":           "settings.prompt.settings_ai_default_split",
	"Settings > AI Settings > Enabled agents > ":               "settings.prompt.settings_ai_enabled_agents",
	"Settings > AI Settings > Resume picker > ":                "settings.prompt.settings_ai_resume_picker",
	"Settings > AI Settings > Resume picker > Picker limit > ": "settings.prompt.settings_ai_resume_picker_limit",
	"Settings > AI Settings > Resume picker > Scan depth > ":   "settings.prompt.settings_ai_resume_picker_depth",
	"Resume picker limit > ":                                   "settings.prompt.resume_picker_limit",
	"Resume scan depth > ":                                     "settings.prompt.resume_scan_depth",
	"Settings > Notifications > Hook quiet policy > ":          "settings.prompt.settings_hook_quiet_policy",
	"Settings > Notifications > Delivery sources > ":           "settings.prompt.settings_delivery_sources",
	"Settings > Appearance > Path icon > ":                     "settings.prompt.settings_appearance_path_icon",
	"Settings > Appearance > Git icon > ":                      "settings.prompt.settings_appearance_git_icon",
	"Settings > Appearance > Notify icon > ":                   "settings.prompt.settings_appearance_notify_icon",
	"Settings > Appearance > AI badge style > ":                "settings.prompt.settings_appearance_ai_badge_style",
	"Settings > Theme > Global > ":                             "settings.prompt.settings_theme_global",
	"Preset > ":                                                "settings.prompt.preset",
	"Auto-save interval > ":                                    "settings.prompt.autosave_interval",
	"Snapshot name > ":                                         "settings.prompt.snapshot_name",
	"Settings > Keybindings > ":                                "settings.prompt.settings_keybindings",
	"Settings > Keybindings > Action > ":                       "settings.prompt.settings_keybindings_action",
	"Enter key > ":                                             "settings.prompt.enter_key",
	"Settings > Session State > Sidebar startup picker > ":     "settings.prompt.settings_session_state_sidebar_startup",
	"Settings > Labs > Project Hooks > ":                       "settings.prompt.settings_labs_project_hooks",
	"Settings > Appearance > Language / Locale > ":             "settings.prompt.settings_appearance_language_locale",

	"Project Picker":        "settings.text.project_picker",
	"AI Settings":           "settings.text.ai_settings",
	"Enabled agents":        "settings.text.enabled_agents",
	"Notifications":         "settings.root.notifications",
	"Hooks":                 "settings.text.hooks",
	"Appearance":            "settings.root.appearance",
	"Theme":                 "settings.text.theme",
	"Session State":         "settings.text.session_state",
	"Keybindings":           "settings.text.keybindings",
	"Labs":                  "settings.text.labs",
	"About":                 "settings.text.about",
	"Trust":                 "settings.text.trust",
	"Hooks (project)":       "settings.text.hooks_project",
	"Project recipe":        "settings.text.project_recipe",
	"Effective merge view":  "settings.text.effective_merge_view",
	"Project context":       "settings.text.project_context",
	"Feedback":              "settings.text.feedback",
	"complete":              "settings.text.complete",
	"failed":                "settings.text.failed",
	"Language / Locale":     "settings.text.language_locale",
	"Warning":               "settings.text.warning",
	"Current":               "settings.text.current",
	"Action":                "settings.text.action",
	"Action ID":             "settings.text.action_id",
	"Actions":               "settings.text.actions",
	"Options":               "settings.text.options",
	"Keys":                  "settings.text.keys",
	"Key":                   "settings.text.key",
	"Surface":               "settings.text.surface",
	"Tier":                  "settings.text.tier",
	"Delivery path":         "settings.text.delivery_path",
	"Default transport key": "settings.text.default_transport_key",
	"Plain aliases":         "settings.text.plain_aliases",
	"Editing":               "settings.text.editing",
	"Terminal":              "settings.text.terminal",
	"Aliases":               "settings.text.aliases",
	"Version":               "settings.text.version",
	"Source":                "settings.text.source",
	"App":                   "settings.text.app",
	"Tmux actions":          "settings.text.tmux_actions",
	"Key setup":             "settings.text.key_setup",
	"Diagnose keys":         "settings.text.diagnose_keys",
	"Terminal remediation":  "settings.text.terminal_remediation",
	"Diagnostics":           "settings.text.diagnostics",
	"Dependencies":          "settings.text.dependencies",
	"Rename key":            "settings.text.rename_key",
	"Windows Term.":         "settings.text.windows_term",
	"Docs":                  "settings.text.docs",
	"Update":                "settings.text.update",
	"Latest":                "settings.text.latest",
	"Update state":          "settings.text.update_state",
	"Installer":             "settings.text.installer",
	"Release notes":         "settings.text.release_notes",
	"Add key":               "settings.text.add_key",
	"+ Add key":             "settings.text.add_key_plus",
	"Press a key":           "settings.text.press_a_key",
	"Cancel":                "settings.text.cancel",
	"Advanced...":           "settings.text.advanced_ellipsis",
	"Enter key name":        "settings.text.enter_key_name_title",
	"Raw diagnostic view":   "settings.text.raw_diagnostic_view",
	"Remove key":            "settings.text.remove_key",
	"Test key":              "settings.text.test_key",
	"Unbind":                "settings.text.unbind",
	"Reset to default":      "settings.text.reset_to_default",
	"Use default":           "settings.text.use_default",
	"Troubleshooting":       "settings.text.troubleshooting",
	"Default":               "settings.text.default_state",
	"Custom":                "settings.text.custom_state",
	"Available":             "settings.text.available_state",
	"Unbound":               "settings.text.unbound_state",

	"Native macOS keybindings": "settings.text.native_macos_keybindings",
	"Live system resources":    "settings.text.live_system_resources",

	"project roots, workdirs, and pins":                               "settings.text.project_roots_workdirs_pins",
	"default split mode":                                              "settings.text.default_split_mode",
	"default split mode, enabled agents":                              "settings.text.default_split_mode_enabled_agents",
	"desktop mode, delivery sources, and hook quiet policy":           "settings.text.notification_surfaces",
	"global lifecycle hook paths":                                     "settings.text.global_lifecycle_hooks",
	"theme font status and icon decoration":                           "settings.text.theme_font_status_icon_decoration",
	"language, AI badge, status/notify icon decoration":               "settings.text.theme_font_status_icon_decoration",
	"global preset, color tokens, and font hints":                     "settings.text.global_theme_settings",
	"edit tmux plain and prefix chords":                               "settings.text.edit_tmux_chords",
	"Actions are listed with active keys and state.":                  "settings.text.keybindings_actions_list",
	"on - modified chords only, processed locally":                    "settings.text.native_keys_on_desc",
	"off - broker and Accessibility prompt disabled":                  "settings.text.native_keys_off_desc",
	"off - PROJMUX_NATIVE_KEYS override":                              "settings.text.native_keys_env_override_desc",
	"press desired key":                                               "settings.text.press_desired_key",
	"capture desired key":                                             "settings.text.capture_desired_key",
	"physical capture unavailable on this platform - type a key name": "settings.text.physical_capture_unavailable_desc",
	"enter key name":                                                  "settings.text.enter_key_name",
	"record and confirm a key combination":                            "settings.text.record_confirm_key_combination",
	"type literal or nonstandard tmux key name":                       "settings.text.type_literal_nonstandard_key_name",
	"type a tmux key name":                                            "settings.text.type_tmux_key_name",
	"return to action":                                                "settings.text.return_to_action",
	"advanced options":                                                "settings.text.advanced_options",
	"remove all active keys":                                          "settings.text.remove_all_active_keys",
	"choose a row below":                                              "settings.text.choose_row_below",
	"key detail":                                                      "settings.text.key_detail",
	"active key":                                                      "settings.text.active_key",
	"active keys":                                                     "settings.text.active_keys",
	"no active keys":                                                  "settings.text.no_active_keys",
	"Not bound":                                                       "settings.text.not_bound",
	"diagnostic":                                                      "settings.text.diagnostic",
	"view only":                                                       "settings.text.view_only",
	"Test key delivery, Advanced...":                                  "settings.text.test_key_delivery_advanced",
	"experimental features":                                           "settings.text.experimental_features",
	"unavailable on this platform":                                    "settings.text.live_resources_unavailable_desc",
	"off - hidden; current system view":                               "settings.text.live_resources_off_desc",
	"on - live CPU and memory; current system view":                   "settings.text.live_resources_on_desc",
	"version, updates, key setup":                                     "settings.text.version_updates_key_setup",
	"version, updates, welcome, and quit":                             "settings.text.version_updates_key_setup",
	"open Settings from a managed project to enable project actions":  "settings.text.open_managed_project_for_actions",
	"disabled - no project context":                                   "settings.text.disabled_no_project_context",
	"declare env, kube, startup":                                      "settings.text.declare_env_kube_startup",
	"global + project merge with source labels":                       "settings.text.global_project_merge_sources",
	"folder marker before cwd":                                        "settings.text.folder_marker_before_cwd",
	"provider marker before branch":                                   "settings.text.provider_marker_before_branch",
	"bell marker in notification sidebar":                             "settings.text.bell_marker_notification_sidebar",
	"AI badge style":                                                  "settings.text.ai_badge_style",
	"pane border live AI marker":                                      "settings.text.pane_border_live_ai_marker",
	"Nerd Font-style marker":                                          "settings.text.nerd_font_marker",
	"emoji marker":                                                    "settings.text.emoji_marker",
	"no icon prefix":                                                  "settings.text.no_icon_prefix",
	"colored dot marker":                                              "settings.text.colored_dot_marker",
	"preserve spacing without marker":                                 "settings.text.preserve_spacing_without_marker",

	"Desktop notifications":              "settings.notifications.desktop",
	"Delivery sources":                   "settings.notifications.delivery_sources",
	"AI notification dedupe":             "settings.text.ai_notification_dedupe",
	"Desktop sender override":            "settings.text.desktop_sender_override",
	"Scope":                              "settings.text.scope",
	"Runtime config":                     "settings.text.runtime_config",
	"Install":                            "settings.text.install",
	"Status":                             "settings.text.status",
	"Config path":                        "settings.text.config_path",
	"Conflict":                           "settings.text.conflict",
	"Tested version":                     "settings.text.tested_version",
	"Notice":                             "settings.text.notice",
	"Install command":                    "settings.text.install_command",
	"Remove command":                     "settings.text.remove_command",
	"Dry-run command":                    "settings.text.dry_run_command",
	"Copy only":                          "settings.text.copy_only",
	"Enter copies":                       "settings.text.enter_copies",
	"Default split mode":                 "settings.text.default_split_mode_title",
	"Current Project Session":            "settings.text.current_project_session",
	"Session Popup: Open Session State":  "settings.text.session_popup_open_session_state",
	"previously active pane / last pane": "settings.text.previously_active_pane_last_pane",
	"Welcome":                            "settings.about.welcome",
	"Quit projmux":                       "settings.text.quit_projmux",
	"Update Now":                         "settings.text.update_now",
	"Check Updates":                      "settings.text.check_updates",
	"Project Root":                       "settings.text.project_root",
	"Workdirs":                           "settings.text.workdirs",
	"Pinned Projects":                    "settings.text.pinned_projects",
	"Add Project...":                     "settings.text.add_project",
	"Add Current Project":                "settings.text.add_current_project",
	"Use Current Project as Root":        "settings.text.use_current_project_root",
	"Set Project Root...":                "settings.text.set_project_root",
	"Clear Saved Project Root":           "settings.text.clear_saved_project_root",
	"Type path manually...":              "settings.text.type_path_manually",
	"Add Workdir...":                     "settings.text.add_workdir",
	"Remove":                             "settings.text.remove",
	"Clear all pins":                     "settings.text.clear_all_pins",
	"Back":                               "settings.text.back",
	"Saved workdirs":                     "settings.text.saved_workdirs",
	"Effective Project Root":             "settings.text.effective_project_root",
	"Saved Project Root":                 "settings.text.saved_project_root",
	"Env":                                "settings.text.env",

	"add or remove scan roots":                      "settings.text.add_remove_scan_roots",
	"add or remove pins":                            "settings.text.add_remove_pins",
	"unavailable":                                   "settings.text.unavailable",
	"not configured":                                "settings.text.not_configured",
	"home unavailable":                              "settings.text.home_unavailable",
	"no project context":                            "settings.text.no_project_context",
	"pins unavailable":                              "settings.text.pins_unavailable",
	"already pinned":                                "settings.text.already_pinned",
	"scan filesystem roots":                         "settings.text.scan_filesystem_roots",
	"(no pinned projects)":                          "settings.text.no_pinned_projects",
	"skip filesystem scan":                          "settings.text.skip_filesystem_scan",
	"append a directory to the saved workdirs list": "settings.text.append_workdir",
	"not set":                                       "settings.text.not_set",
	"active":                                        "settings.text.active",
	"saved":                                         "settings.text.saved",
	"save one primary root path directly":           "settings.text.save_primary_root_directly",
	"remove ~/.config/projmux/projdir":              "settings.text.remove_saved_projdir",
	"no env, tmux option, or saved value":           "settings.text.no_env_tmux_saved",
	"env, read-only":                                "settings.text.env_read_only",
	"Project Root is the primary root. Workdirs are extra search roots. Set PROJMUX_PROJDIR, @projmux_projdir, or the saved ~/.config/projmux/projdir value.": "settings.text.project_root_hint",
	"Env PROJMUX_PROJDIR and tmux @projmux_projdir override the saved value until unset.":                                                                     "settings.text.project_root_override_hint",
	"(none)":                      "settings.text.none_parenthesized",
	"consume pending notify rows": "settings.text.consume_pending_notify_rows",
	"statusbar/sidebar":           "settings.text.statusbar_sidebar",
	"silence OS notifications; in-app notify queue is unaffected":                   "settings.text.desktop_notify_none_desc",
	"fire toast / notify-send for AI reply-ready without click-to-focus":            "settings.text.desktop_notify_notify_desc",
	"fire toast with click-to-focus and auto-raise host terminal via osfocus chain": "settings.text.desktop_notify_raise_desc",
	"desktop AI notifications":                                 "settings.text.desktop_ai_notifications",
	"tmux bell fallback stays 5s":                              "settings.text.tmux_bell_fallback_5s",
	"collapse duplicate desktop AI notifications":              "settings.text.collapse_duplicate_desktop_ai_notifications",
	"Custom seconds":                                           "settings.text.custom_seconds",
	"Resume picker":                                            "settings.text.resume_picker",
	"Picker limit":                                             "settings.text.ai_resume_picker_limit_row",
	"Scan depth":                                               "settings.text.ai_resume_picker_depth_row",
	"Resume picker limit":                                      "settings.text.resume_picker_limit",
	"max sessions listed in the AI resume picker":              "settings.text.resume_picker_limit_desc",
	"Custom limit":                                             "settings.text.custom_limit",
	"store a session count between 1 and 100":                  "settings.text.resume_picker_custom_desc",
	"Resume scan depth":                                        "settings.text.resume_scan_depth",
	"include sessions from cwd child directories":              "settings.text.resume_scan_depth_desc",
	"Custom depth":                                             "settings.text.custom_depth",
	"store a cwd child depth between 0 and 8":                  "settings.text.resume_scan_depth_custom_desc",
	"store a positive seconds value":                           "settings.text.store_positive_seconds_value",
	"catalog defaults":                                         "settings.text.catalog_defaults",
	"install events stay in catalog":                           "settings.text.install_events_stay_catalog",
	"runtime action only":                                      "settings.text.runtime_action_only",
	"install field is unchanged":                               "settings.text.install_field_unchanged",
	"unchanged":                                                "settings.text.unchanged",
	"Settings only changes runtime action":                     "settings.text.settings_only_changes_runtime_action",
	"use embedded or local catalog action":                     "settings.text.use_embedded_or_local_catalog_action",
	"update pane state without notification delivery":          "settings.text.update_pane_state_without_notification_delivery",
	"mark hook-active and log only":                            "settings.text.mark_hook_active_log_only",
	"in-app queue + OS toast supported by specialized handler": "settings.text.hook_notify_desktop_supported",
	"generic in-app queue only; OS toast unsupported":          "settings.text.hook_notify_generic_only",
	"no notify handler; falls back to hook-active log only":    "settings.text.hook_notify_no_handler",
	"revisit the shell quickstart guide":                       "settings.text.revisit_shell_guide",
	"open quit actions":                                        "settings.text.open_quit_actions",
	"run installer-specific update command":                    "settings.text.run_installer_update",
	"refresh cached GitHub release metadata":                   "settings.text.refresh_github_release_metadata",
	"status unavailable":                                       "settings.text.status_unavailable",
	"unreadable":                                               "settings.text.unreadable",
	"global config unreadable":                                 "settings.text.global_config_unreadable",
	"warning":                                                  "settings.text.warning_lower",
	"current":                                                  "settings.text.current_lower",
	"env override":                                             "settings.text.env_override",
	"built-in fallback":                                        "settings.text.built_in_fallback",
	"explicit override":                                        "settings.text.explicit_override",
	"English UI":                                               "settings.text.english_ui",
	"Korean UI":                                                "settings.text.korean_ui",
	"detect from LC_ALL, LC_MESSAGES, LANG":                    "settings.text.detect_locale_env",
	"unsupported":                                              "settings.text.unsupported",
	"Path icon":                                                "settings.text.path_icon",
	"Git icon":                                                 "settings.text.git_icon",
	"Notify icon":                                              "settings.text.notify_icon",
	"Pending Notifications":                                    "settings.text.pending_notifications",
	"Preview":                                                  "settings.text.preview",
	"Set":                                                      "settings.text.set",
	"Preset selector":                                          "settings.text.preset_selector",
	"Reset theme values":                                       "settings.text.reset_theme_values",
	"Core":                                                     "settings.text.theme_group_core",
	"Surfaces":                                                 "settings.text.theme_group_surfaces",
	"App chrome":                                               "settings.text.theme_group_app_chrome",
	"background, foreground, accent":                           "settings.text.theme_group_core_desc",
	"panels, selected rows, muted text":                        "settings.text.theme_group_surfaces_desc",
	"severity and AI status colors":                            "settings.text.theme_group_state_desc",
	"active-pane tint and focus border":                        "settings.text.theme_group_app_chrome_desc",
	"Inherit global preset":                                    "settings.text.inherit_global_preset",
	"Clear global preset":                                      "settings.text.clear_global_preset",
	"Inherit global":                                           "settings.text.inherit_global",
	"Use preset value":                                         "settings.text.use_preset_value",
	"Type hex value...":                                        "settings.text.type_hex_value",
	"Pick from 256-color grid...":                              "settings.text.pick_from_color_grid",
	"browse swatches with a live preview":                      "settings.text.color_grid_description",
	"Swatch":                                                   "settings.text.swatch",
	"Global parse error":                                       "settings.text.global_parse_error",
	"Project parse error":                                      "settings.text.project_parse_error",
	"Global config":                                            "settings.text.global_config",
	"Project config":                                           "settings.text.project_config",
	"parse error":                                              "settings.text.parse_error",
	"unresolved path":                                          "settings.text.unresolved_path",
	"missing or empty":                                         "settings.text.missing_or_empty",
	"loaded":                                                   "settings.text.loaded",
	"  (no hooks configured)":                                  "settings.text.no_hooks_configured",
	"  (no entries)":                                           "settings.text.no_entries",
	"(unset)":                                                  "settings.text.unset_parenthesized",
	"Auto-save":                                                "settings.text.auto_save",
	"Storage":                                                  "settings.text.storage",
	"Retention":                                                "settings.text.retention",
	"latest snapshot store":                                    "settings.text.latest_snapshot_store",
	"per-session JSON under XDG state":                         "settings.text.per_session_json_xdg_state",
	"latest snapshot only":                                     "settings.text.latest_snapshot_only",
	"named snapshots are manual project files":                 "settings.text.named_snapshots_manual_project_files",
	"Project path":                                             "settings.text.project_path",
	"Session identity":                                         "settings.text.session_identity",
	"Project auto-save":                                        "settings.text.project_auto_save",
	"Effective auto-save":                                      "settings.text.effective_auto_save",
	"Global auto-save":                                         "settings.text.global_auto_save",
	"Auto-save interval":                                       "settings.text.auto_save_interval",
	"Snapshot actions":                                         "settings.text.snapshot_actions",
	"Interval":                                                 "settings.text.interval",
	"Snapshot":                                                 "settings.text.snapshot",
	"Save latest snapshot":                                     "settings.text.save_latest_snapshot",
	"Preview restore":                                          "settings.text.preview_restore",
	"Delete snapshot":                                          "settings.text.delete_snapshot",
	"capture live project session as latest":                   "settings.text.capture_live_project_session_latest",
	"choose a name for the live project session":               "settings.text.choose_name_live_project_session",
	"dry-run only":                                             "settings.text.dry_run_only",
	"unavailable without a valid snapshot":                     "settings.text.unavailable_without_valid_snapshot",
	"follow global auto-save":                                  "settings.text.follow_global_auto_save",
	"enable latest snapshot auto-save for this project":  "settings.text.enable_project_latest_snapshot_autosave",
	"disable latest snapshot auto-save for this project": "settings.text.disable_project_latest_snapshot_autosave",
	"enable auto-save":  "settings.text.enable_auto_save",
	"disable auto-save": "settings.text.disable_auto_save",
	"Unsupported locale {locale} from {source}; using {fallback}.":                         "settings.text.unsupported_locale_warning",
	"Choose the default split mode for future AI launches.":                                "settings.footer.choose_default_ai_split",
	"Choose an agent or shell target to launch.":                                           "settings.footer.choose_agent_or_shell",
	"Session state overview is read-only here.":                                            "settings.footer.session_state_read_only",
	"Editable rows write keymap aliases. View-only rows explain transport-dependent keys.": "settings.footer.keymap_editable_rows",
	"Use a tmux plain chord such as C-r, M-a, M-S-Left, or C-Space.":                       "settings.footer.tmux_plain_chord_examples",
	"Enter a key such as C-r, M-a, M-S-Left, or C-Space.":                                  "settings.footer.enter_custom_key_examples",
	"Enter confirms · Esc cancels · Advanced typed entry is available from Action detail.": "settings.footer.keybindings_recorder",
	"Use the Back row or picker close action to close":                                     "settings.footer.back_row_or_close",

	"Enter: open  |  Back row: parent":                                        "settings.footer.enter_open_back_parent",
	"Enter: open/apply  |  Back row: parent":                                  "settings.footer.enter_open_apply_back_parent",
	"Enter: apply  |  Back row: parent":                                       "settings.footer.enter_apply_back_parent",
	"Enter: toggle  |  Back row: parent":                                      "settings.footer.enter_toggle_back_parent",
	"Enter: apply preset  |  Back row: parent":                                "settings.footer.enter_apply_preset_back_parent",
	"Enter: apply swatch or open hex input  |  Back row: parent":              "settings.footer.enter_apply_swatch_hex_back_parent",
	"Enter: pick swatch  |  h: hex input  |  Esc: back":                       "settings.footer.color_grid",
	"Arrows: move  |  Enter: pick swatch  |  h: hex input":                    "settings.header.color_grid",
	"Enter: edit/add  |  Back row: parent":                                    "settings.footer.enter_edit_add_back_parent",
	"Enter: action  |  Back row: parent":                                      "settings.footer.enter_action_back_parent",
	"Enter: add  |  Back row: parent":                                         "settings.footer.enter_add_back_parent",
	"Enter: save":                                                             "settings.footer.enter_save",
	"Enter: save #RRGGBB":                                                     "settings.footer.enter_save_hex_color",
	"Enter: add":                                                              "settings.footer.enter_add",
	"Enter: open/add/remove  |  Back row: parent":                             "settings.footer.enter_open_add_remove_back_parent",
	"Enter: save  |  Back row: parent":                                        "settings.footer.enter_save_back_parent",
	"Enter: save  |  Example: 120":                                            "settings.footer.enter_save_example_120",
	"Enter: save  |  Example: 30":                                             "settings.footer.enter_save_example_30",
	"Enter: save  |  Example: 2":                                              "settings.footer.enter_save_example_2",
	"Enter: save  |  Examples: 30s, 2m, 90":                                   "settings.footer.enter_save_examples_interval",
	"Enter: start  |  Back row: projects  |  Esc: empty session":              "settings.footer.enter_start_back_projects_esc_empty",
	"Enter: choose  |  Esc: continue shell":                                   "settings.footer.enter_choose_esc_continue_shell",
	"Enter: choose  |  Esc: cancel":                                           "settings.footer.enter_choose_esc_cancel",
	"Enter: confirm":                                                          "settings.footer.enter_confirm",
	"Enter: back  |  Back row: parent":                                        "settings.footer.enter_back_back_parent",
	"Enter: view hooks  |  Back row: parent":                                  "settings.footer.enter_view_hooks_back_parent",
	"Enter: change action  |  Back row: parent":                               "settings.footer.enter_change_action_back_parent",
	"Enter: view details  |  Back row: parent":                                "settings.footer.enter_view_details_back_parent",
	"Enter: copy command  |  Back row: parent":                                "settings.footer.enter_copy_command_back_parent",
	"Enter: diagnose  |  Back row: parent":                                    "settings.footer.enter_diagnose_back_parent",
	"Enter: probe/apply  |  Back row: parent":                                 "settings.footer.enter_probe_apply_back_parent",
	"Enter: edit/apply  |  Back row: parent":                                  "settings.footer.enter_edit_apply_back_parent",
	"Enter: action  |  Esc/Ctrl-C: close":                                     "settings.footer.enter_action_esc_ctrl_c_close",
	"Back row: About  |  Esc: close settings":                                 "settings.footer.back_about_esc_close",
	"Current aliases, terminal delivery, and conflicts are shown per action.": "settings.footer.keybindings_summary_legacy",
	"Current keybindings and aliases are shown per action.":                   "settings.footer.keybindings_summary",
	"Actions show their active keys and current state.":                       "settings.footer.keybindings_actions_state",
	"Manage the active keys for this action.":                                 "settings.footer.keybindings_action_detail",
	"Press a key to add it to this action.":                                   "settings.footer.keybindings_add_key",
	"Physical key capture is unavailable here; enter a key name.":             "settings.footer.keybindings_add_key_typed",
	"Typed key names and raw diagnostics stay advanced.":                      "settings.footer.keybindings_add_key_advanced",
	"Manage this active key.":                                                 "settings.footer.keybindings_key_detail",

	// --- Non-settings picker chrome (notify, switch pins, hookmaker) -------
	// These render through the shared picker choke point or source-level
	// localization. Keys live under the settings.* / picker.* namespaces so
	// the existing ko-KR coverage test enforces translations.
	"Notify > ":                        "picker.notify.prompt",
	"Newest first":                     "picker.notify.header_newest_first",
	"No pending notifications":         "picker.notify.empty",
	"focus live/inactive / clean gone": "picker.notify.action.focus_clean",
	"ack child":                        "picker.notify.action.ack_child",
	"ack group":                        "picker.notify.action.ack_group",
	"clear non-critical":               "picker.notify.action.clear_non_critical",
	"clear all":                        "picker.notify.action.clear_all",
	"show child rows":                  "picker.notify.action.show_child_rows",
	"hide child rows":                  "picker.notify.action.hide_child_rows",

	"pin project":         "picker.switch.action.pin_project",
	"kill session":        "picker.switch.action.kill_session",
	"+ Add pin...":        "picker.switch.pin.add_interactive",
	"+ Add current pin  ": "picker.switch.pin.add_current",
	"x Clear all pins":    "picker.switch.pin.clear_all",
	"x Remove  ":          "picker.switch.pin.remove",

	"Defined in":       "settings.text.hook_defined_in",
	"run":              "settings.text.hook_run",
	"Read-only":        "settings.text.hook_read_only",
	"Project override": "settings.text.hook_project_override",

	// Breadcrumb/path and title segments surfaced by the picker-chrome audit
	// as bypassing the catalog. Registering the static segments lets the
	// composed path/`-`-split resolver translate the full prompt/title.
	"AI Setting":                               "picker.ai.crumb_setting",
	"AI Launch":                                "picker.ai.crumb_launch",
	"AI Launch - Split direction: ":            "picker.ai.launch_split_direction_title",
	"AI Resume":                                "picker.ai.crumb_resume",
	"AI Resume - Split direction: ":            "picker.ai.resume_split_direction_title",
	"AI Resume > ":                             "picker.ai.resume_prompt",
	"Showing latest %d resume sessions.":       "picker.ai.resume_showing_latest",
	"Showing latest %d of %d resume sessions.": "picker.ai.resume_showing_latest_of",
	"Read-only hook":                           "picker.hook.read_only_hook",
	"Projects":                                 "picker.crumb.projects",
	"Sessions":                                 "picker.crumb.sessions",
	"State":                                    "picker.crumb.state",
	"Session state opens read-only; destructive actions keep the current confirmation policy.": "picker.sessions.state_readonly_note",
}

// settingsTextKeys preserves the historical name for the shared registry so
// existing tests and call sites keep compiling. New code should reference
// uiTextKeys / localizeUIText directly.
var settingsTextKeys = uiTextKeys

// settingsLocale resolves the active UI locale for package-level eager
// localization helpers that have no *settingsCommand (and therefore no
// injected homeDir) — e.g. projmuxFooter, settingsLabel, and the settings root
// chips/prompts/entries. It MUST pass os.UserHomeDir, not nil: appLocale only
// honors the global config `[ui] locale` override when it can read the global
// config, and a nil homeDir makes appGlobalLocaleOverride return "" so the
// override is silently dropped and resolution falls through to the ambient
// LANG. That nil was the bug behind footers/labels rendering Korean for a user
// who pinned `[ui] locale = "en-US"` under a ko_KR terminal. The command-bound
// path already does the right thing via (*settingsCommand).locale().
func settingsLocale() i18n.Locale {
	return appLocale(os.UserHomeDir, os.Getenv)
}

// localizeUIText is the shared UI localization entry point. Given the active
// locale and an English fallback literal, it resolves the literal through the
// catalog (exact, then composed/path/prefix forms) and returns the localized
// string, or the fallback verbatim when no key matches.
//
// It is idempotent for already-translated strings: a Korean string is not a
// registered English literal, so it never matches a key and is returned as-is.
// This is what lets the common picker choke point localize without
// double-translating strings that Settings already localized.
func localizeUIText(locale i18n.Locale, fallback string) string {
	return settingsCatalogTextLocale(locale, fallback)
}

func settingsCatalogText(fallback string) string {
	return settingsCatalogTextLocale(settingsLocale(), fallback)
}

func settingsCatalogTextLocale(locale i18n.Locale, fallback string) string {
	if text, ok := settingsCatalogExactTextLocale(locale, fallback); ok {
		return text
	}
	if text, ok := settingsCatalogComposedTextLocale(locale, fallback); ok {
		return text
	}
	return fallback
}

func settingsCatalogExactTextLocale(locale i18n.Locale, fallback string) (string, bool) {
	key, ok := uiTextKeys[fallback]
	if !ok {
		key, ok = uiTextKeys[strings.TrimSpace(fallback)]
	}
	if !ok {
		return "", false
	}
	return localizeText(locale, key, fallback), true
}

func settingsCatalogExactTextOrFallbackLocale(locale i18n.Locale, fallback string) string {
	if text, ok := settingsCatalogExactTextLocale(locale, fallback); ok {
		return text
	}
	return fallback
}

func settingsCatalogComposedTextLocale(locale i18n.Locale, fallback string) (string, bool) {
	if strings.TrimSpace(fallback) == "" {
		return "", false
	}
	if strings.Contains(fallback, " > ") || strings.HasSuffix(fallback, "> ") {
		if text, ok := settingsCatalogPathTextLocale(locale, fallback); ok {
			return text, true
		}
	}
	if strings.Contains(fallback, " - ") {
		parts := strings.Split(fallback, " - ")
		changed := false
		for i, part := range parts {
			if text, ok := settingsCatalogExactTextLocale(locale, part); ok {
				parts[i] = text
				changed = true
			} else if text, ok := settingsCatalogPrefixTextLocale(locale, part); ok {
				parts[i] = text
				changed = true
			} else if before, ok0 := strings.CutSuffix(part, "Pending Notifications"); ok0 {
				parts[i] = before + settingsCatalogTextLocale(locale, "Pending Notifications")
				changed = true
			}
		}
		if changed {
			return strings.Join(parts, " - "), true
		}
	}
	if text, ok := settingsCatalogPrefixTextLocale(locale, fallback); ok {
		return text, true
	}
	if before, ok := strings.CutSuffix(fallback, " hooks"); ok {
		prefix := strings.TrimSpace(before)
		if prefix != "" && locale != i18n.FallbackLocale {
			return prefix + " " + settingsCatalogTextLocale(locale, "Hooks"), true
		}
	}
	if before, ok := strings.CutSuffix(fallback, "Pending Notifications"); ok {
		prefix := before
		return prefix + settingsCatalogTextLocale(locale, "Pending Notifications"), true
	}
	return "", false
}

func settingsCatalogPathTextLocale(locale i18n.Locale, fallback string) (string, bool) {
	trailing := strings.HasSuffix(fallback, "> ")
	rawParts := strings.Split(fallback, ">")
	parts := make([]string, 0, len(rawParts))
	changed := false
	for _, raw := range rawParts {
		part := strings.TrimSpace(raw)
		if part == "" {
			continue
		}
		if text, ok := settingsCatalogExactTextLocale(locale, part); ok {
			parts = append(parts, text)
			changed = true
			continue
		}
		if text, ok := settingsCatalogPrefixTextLocale(locale, part); ok {
			parts = append(parts, text)
			changed = true
			continue
		}
		parts = append(parts, part)
	}
	if !changed || len(parts) == 0 {
		return "", false
	}
	out := strings.Join(parts, " > ")
	if trailing {
		out += " > "
	}
	return out, true
}

func settingsCatalogPrefixTextLocale(locale i18n.Locale, fallback string) (string, bool) {
	for _, prefix := range []string{
		"Preview",
		"Set",
		"Theme",
		"Keybinding",
		"Keybindings",
		"Hook quiet policy",
		"Project auto-save",
		"Auto-save",
		"Sidebar startup picker",
		"Project hooks",
	} {
		if fallback == prefix {
			continue
		}
		if strings.HasPrefix(fallback, prefix+" ") {
			text, ok := settingsCatalogExactTextLocale(locale, prefix)
			if !ok {
				continue
			}
			return text + strings.TrimPrefix(fallback, prefix), true
		}
	}
	return "", false
}
