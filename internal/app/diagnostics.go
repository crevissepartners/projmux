package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/crevissepartners/projmux/internal/diagnostics"
)

type diagnosticsCommand struct {
	lookupEnv func(string) string
	homeDir   func() (string, error)
	doctor    *doctorCommand
	ai        rawArgvCommand
}

func newDiagnosticsCommand() *diagnosticsCommand {
	return &diagnosticsCommand{lookupEnv: os.Getenv, homeDir: os.UserHomeDir, doctor: newDoctorCommand()}
}

func (c *diagnosticsCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printRouteUsage(stderr, "diagnostics")
		return usageError("diagnostics requires a subcommand")
	}
	switch args[0] {
	case "agent-hook":
		return forwardRawArgv(c.ai, "diagnostics agent-hook", "ai", []string{"ingest", "log"}, args[1:], stdout, stderr)
	case "log":
		return c.runLog(args[1:], stdout, stderr)
	case "report":
		return c.runReport(args[1:], stdout, stderr)
	default:
		printRouteUsage(stderr, "diagnostics")
		return usageError(fmt.Sprintf("unknown diagnostics subcommand: %s", args[0]))
	}
}

func (c *diagnosticsCommand) runLog(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("diagnostics log", flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	tail := fs.Int("tail", 50, "number of recent records to print")
	jsonOut := fs.Bool("json", false, "print records as JSONL")
	level := fs.String("level", "", "filter by level")
	component := fs.String("component", "", "filter by component")
	pathOnly := fs.Bool("path", false, "print the operations log path")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return flagParseError(err)
	}
	if fs.NArg() != 0 {
		printRouteUsage(stderr, "diagnostics log")
		return usageError("diagnostics log does not accept positional arguments")
	}
	if *tail < 0 {
		return usageError("diagnostics log --tail must be zero or greater")
	}
	*level = strings.ToLower(strings.TrimSpace(*level))
	if *level != "" && !diagnostics.ValidLevel(*level) {
		return usageError("diagnostics log --level must be info, warn, or error")
	}
	*component = strings.TrimSpace(*component)
	path, err := diagnostics.DefaultPath(c.lookupEnv, c.homeDir)
	if err != nil {
		return err
	}
	if *pathOnly {
		_, err := fmt.Fprintln(stdout, path)
		return err
	}
	events, err := diagnostics.NewStore(path).Read()
	if err != nil {
		return fmt.Errorf("read operational diagnostics: %w", err)
	}
	filtered := events[:0]
	for _, event := range events {
		if *level != "" && event.Level != *level {
			continue
		}
		if *component != "" && event.Component != *component {
			continue
		}
		filtered = append(filtered, event)
	}
	if len(filtered) > *tail {
		filtered = filtered[len(filtered)-*tail:]
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	for _, event := range filtered {
		if *jsonOut {
			if err := encoder.Encode(event); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintln(stdout, formatOperationalEvent(event)); err != nil {
			return err
		}
	}
	return nil
}

func formatOperationalEvent(event diagnostics.Event) string {
	parts := []string{event.At, strings.ToUpper(event.Level), event.Component, event.Event, event.Result}
	if event.Command != "" {
		command := event.Command
		if event.Subcommand != "" {
			command += " " + event.Subcommand
		}
		parts = append(parts, "command="+command)
	}
	if event.Operation != "" {
		parts = append(parts, "operation="+event.Operation)
	}
	if event.Source != "" {
		parts = append(parts, "source="+event.Source)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"transition", event.Transition},
		{"disposition", event.Disposition},
		{"provider", event.Provider},
		{"category", event.Category},
		{"route", event.Route},
		{"ai_kind", event.AIKind},
		{"ai_result", event.AIResult},
		{"resource_result", event.ResourceResult},
		{"failure", event.Failure},
		{"decision", event.Decision},
		{"classification", event.Classification},
		{"window_uid", event.WindowUID},
		{"pane_uid", event.PaneUID},
		{"agent_uid", event.AgentUID},
		{"longest_lock_kind", event.LongestLockKind},
		{"longest_lock_step", event.LongestLockStep},
		{"dial_stage", event.DialStage},
	} {
		if field.value != "" {
			parts = append(parts, field.name+"="+field.value)
		}
	}
	for _, count := range []struct {
		name  string
		value *int
	}{
		{"window_count", event.WindowCount},
		{"pane_count", event.PaneCount},
		{"shell_recipe_count", event.ShellRecipeCount},
		{"agent_recipe_count", event.AgentRecipeCount},
		{"startup_recipe_count", event.StartupRecipeCount},
		{"item_count", event.ItemCount},
		{"resumed_count", event.ResumedCount},
		{"skipped_count", event.SkippedCount},
		{"lock_acquisition_count", event.LockAcquisitionCount},
	} {
		if count.value != nil {
			parts = append(parts, fmt.Sprintf("%s=%d", count.name, *count.value))
		}
	}
	parts = append(parts, fmt.Sprintf("duration_ms=%d", event.DurationMS))
	for _, timing := range []struct {
		name  string
		value *int64
	}{
		{"wait_ms", event.WaitMS},
		{"lock_held_ms", event.LockHeldMS},
		{"phase_guard_ms", event.PhaseGuardMS},
		{"phase_first_reconcile_ms", event.PhaseFirstReconcileMS},
		{"phase_operation_ms", event.PhaseOperationMS},
		{"phase_second_reconcile_ms", event.PhaseSecondReconcileMS},
		{"phase_reprove_ms", event.PhaseReproveMS},
		{"phase_store_write_ms", event.PhaseStoreWriteMS},
		{"spawn_to_release_ms", event.SpawnToReleaseMS},
		{"step_keymap_migration_ms", event.StepKeymapMigrationMS},
		{"step_hook_file_migration_ms", event.StepHookFileMigrationMS},
		{"step_retired_file_reclaim_ms", event.StepRetiredFileReclaimMS},
		{"step_route_bind_ms", event.StepRouteBindMS},
		{"step_bell_hook_migration_ms", event.StepBellHookMigrationMS},
		{"step_config_write_ms", event.StepConfigWriteMS},
		{"step_key_sequence_retire_ms", event.StepKeySequenceRetireMS},
		{"step_source_file_ms", event.StepSourceFileMS},
		{"step_route_marker_ms", event.StepRouteMarkerMS},
		{"step_exhausted_replay_ms", event.StepExhaustedReplayMS},
		{"step_converge_ms", event.StepConvergeMS},
		{"lock_wait_total_ms", event.LockWaitTotalMS},
		{"lock_held_total_ms", event.LockHeldTotalMS},
		{"longest_lock_wait_ms", event.LongestLockWaitMS},
		{"longest_lock_held_ms", event.LongestLockHeldMS},
		{"longest_lock_observe_ms", event.LongestLockObserveMS},
		{"longest_lock_plan_ms", event.LongestLockPlanMS},
		{"longest_lock_commit_ms", event.LongestLockCommitMS},
		{"longest_lock_store_write_ms", event.LongestLockStoreWriteMS},
	} {
		if timing.value != nil {
			parts = append(parts, fmt.Sprintf("%s=%d", timing.name, *timing.value))
		}
	}
	parts = append(parts, "run_id="+event.RunID, "version="+event.Version, "mux_backend="+event.MuxBackend)
	if event.Kind != "" {
		parts = append(parts, "kind="+event.Kind)
	}
	if event.Code != "" {
		parts = append(parts, "code="+event.Code)
	}
	if event.Message != "" {
		parts = append(parts, "message="+event.Message)
	}
	return strings.Join(parts, " ")
}
