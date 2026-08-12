package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

const (
	reportSecret  = "secret-REPORT-SEED"
	reportProject = "project-REPORT-SEED"
	reportSession = "session-REPORT-SEED"
	reportWindow  = "window-REPORT-SEED"
	reportPane    = "pane-REPORT-SEED"
	reportThread  = "thread-REPORT-SEED"
	reportRouting = "routing-REPORT-SEED"
	reportUUID    = "beefcafe-1111-2222-3333-deadbeef0000"
	reportArgv    = "--token=argv-REPORT-SEED"
	reportPrompt  = "prompt-REPORT-SEED"
	reportEnv     = "env-REPORT-SEED"
	reportSocket  = "/tmp/socket-REPORT-SEED/projmux-private"
	reportName    = "name-REPORT-SEED"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("preview rejected") }

func testReportCommand(t *testing.T) (*diagnosticsCommand, string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home-REPORT-SEED")
	configHome := filepath.Join(root, "config")
	stateHome := filepath.Join(root, "state")
	project := filepath.Join(root, reportProject)
	for _, dir := range []string{home, configHome, stateHome, project} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	lookup := func(name string) string {
		switch name {
		case "XDG_CONFIG_HOME":
			return configHome
		case "XDG_STATE_HOME":
			return stateHome
		case "PROJMUX_CWD":
			return project
		case "REPORT_SECRET_ENV":
			return reportEnv
		default:
			return ""
		}
	}
	doctor := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	doctor.aiDiagnostics = func() []doctorAINotifyIntegration {
		on := true
		return []doctorAINotifyIntegration{{
			ID: "id-" + reportUUID, Name: reportSecret, ProviderID: "codex", ProviderEnabled: &on,
			Status: doctorAINotifyStatusConflict, ConfigPath: filepath.Join(home, reportSecret),
			StatusLinePath: reportProject, ConflictReason: reportRouting, Guidance: reportPrompt,
			TestedVersion: reportUUID, InstallCommand: reportArgv, RemoveCommand: reportEnv,
			DryRunCommand: reportThread,
		}}
	}
	doctor.resumeDiagnostics = func() []doctorSessionStateResumeDiagnostic {
		return []doctorSessionStateResumeDiagnostic{{
			Session: reportSession, WindowIndex: 17017, PaneIndex: 19019, Agent: "codex", Status: "stale",
			Confidence: "high", ResumeSource: reportRouting, Reason: reportPrompt,
			SnapshotPath: filepath.Join(home, reportWindow, reportPane),
		}}
	}
	doctor.readRuntimeHealth = func(diagnostics.ReadOnlyStore) (diagnostics.RuntimeHealth, error) {
		return diagnostics.RuntimeHealth{
			MuxBackend: diagnostics.MuxBackend(), RecentErrorCount: 2,
			RecentFailureCodes: []diagnostics.Code{diagnostics.CodeTmuxApplyFailed, diagnostics.Code(reportSecret)},
		}, nil
	}
	return &diagnosticsCommand{lookupEnv: lookup, homeDir: func() (string, error) { return home, nil }, doctor: doctor}, stateHome, configHome
}

func seedReportSources(t *testing.T, stateHome, configHome string) (operationsPath, ingestPath, configPath string) {
	t.Helper()
	operationsPath = filepath.Join(stateHome, "projmux", "logs", diagnostics.LogFileName)
	store := diagnostics.NewStore(operationsPath)
	event := diagnosticsFixture(reportUUID, "error", "runtime")
	event.Version = "version-" + reportSecret
	event.Message = strings.Join([]string{reportSecret, reportProject, reportSession, reportWindow, reportPane, reportThread, reportRouting, reportUUID, reportArgv, reportPrompt, reportEnv, reportSocket, reportName}, " ")
	if err := store.Append(event); err != nil {
		t.Fatal(err)
	}
	ingestPath = filepath.Join(stateHome, "projmux", aiIngestLogName)
	if err := os.MkdirAll(filepath.Dir(ingestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	ingest := `{"at":"2026-08-13T00:00:00Z","source":"codex-hook","event":"Stop","result":"error","reason":"` + reportSecret + `","pane":"` + reportPane + `","cwd":"` + reportProject + `","thread_id":"` + reportThread + `","session_id":"` + reportSession + `","turn_id":"` + reportUUID + `"}` + "\n"
	if err := os.WriteFile(ingestPath, []byte(ingest), 0o644); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(configHome, "projmux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("secret = \""+reportSecret+"\"\nenv = \""+reportEnv+"\"\nargv = \""+reportArgv+"\"\nprompt = \""+reportPrompt+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return operationsPath, ingestPath, configPath
}

func readSupportArchive(t *testing.T, path string) (map[string][]byte, []byte) {
	t.Helper()
	compressed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	entries := map[string][]byte{}
	var all bytes.Buffer
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = data
		all.Write(data)
		if header.Mode != 0o600 || !header.ModTime.Equal(time.Unix(0, 0)) {
			t.Fatalf("entry %q mode=%o modtime=%v", header.Name, header.Mode, header.ModTime)
		}
	}
	return entries, all.Bytes()
}

func TestDiagnosticsReportPreviewArchiveManifestPermissionsAndRedaction(t *testing.T) {
	cmd, stateHome, configHome := testReportCommand(t)
	operationsPath, ingestPath, configPath := seedReportSources(t, stateHome, configHome)
	if runtime.GOOS != "windows" {
		for _, path := range []string{operationsPath, ingestPath, configPath} {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	output := filepath.Join(t.TempDir(), reportRouting, "report.tar.gz")
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"report", "--output", output}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	preview := stdout.Bytes()
	for _, want := range []string{"projmux diagnostics report preview", "destination: sha256:", "manifest.json: included", "doctor.json: included", "operational-errors.json: included", "ai-ingest-summary.json: included", "created: sha256:"} {
		if !bytes.Contains(preview, []byte(want)) {
			t.Fatalf("preview missing %q:\n%s", want, preview)
		}
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode = %o", info.Mode().Perm())
	}
	if runtime.GOOS != "windows" {
		parentInfo, err := os.Stat(filepath.Dir(output))
		if err != nil || parentInfo.Mode().Perm() != 0o700 {
			t.Fatalf("new output parent mode = %v, err=%v", parentInfo.Mode().Perm(), err)
		}
	}
	entries, archiveText := readSupportArchive(t, output)
	for _, name := range []string{"manifest.json", "system.json", "doctor.json", "config-presence.json", "operational-errors.json", "ai-ingest-summary.json"} {
		if entries[name] == nil {
			t.Fatalf("missing archive entry %q", name)
		}
	}
	var manifest supportManifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ReportSchemaVersion != 2 || manifest.RedactionMode != supportRedactionMode {
		t.Fatalf("manifest = %#v", manifest)
	}
	assertManifestReason(t, manifest, "doctor.json", "included", "doctor-schema-version-2")
	var doctor map[string]any
	if err := json.Unmarshal(entries["doctor.json"], &doctor); err != nil {
		t.Fatal(err)
	}
	if doctor["schema_version"] != float64(doctorSchemaVersion) {
		t.Fatalf("doctor schema_version = %#v", doctor["schema_version"])
	}
	resume, ok := doctor["session_state_resume"].([]any)
	if !ok || len(resume) != 1 {
		t.Fatalf("doctor session_state_resume = %#v", doctor["session_state_resume"])
	}
	resumeRow, ok := resume[0].(map[string]any)
	if !ok {
		t.Fatalf("doctor resume row = %#v", resume[0])
	}
	for _, field := range []string{"window_index", "pane_index"} {
		value, ok := resumeRow[field].(string)
		if !ok || !strings.HasPrefix(value, "sha256:") {
			t.Fatalf("doctor redacted routing field %s = %#v, want hash string", field, resumeRow[field])
		}
	}
	if bytes.Contains(entries["doctor.json"], []byte(`"window_index": 17017`)) || bytes.Contains(entries["doctor.json"], []byte(`"pane_index": 19019`)) {
		t.Fatalf("doctor numeric routing ID survived redaction:\n%s", entries["doctor.json"])
	}
	configCount := -1
	for _, entry := range manifest.Entries {
		if entry.Name == "config-presence.json" {
			configCount = entry.RecordCount
		}
	}
	if configCount != 2 {
		t.Fatalf("manifest config structural numeric count = %d, want 2: %#v", configCount, manifest.Entries)
	}
	for _, safe := range []string{`"name": "tmux"`, `"name": "git"`, `"status": "ok"`, `"provider_id": "codex"`, `"agent": "codex"`, `"confidence": "high"`, `"severity": "warning"`, `"code": "runtime.socket.unreachable"`, `"remediation": "start-projmux-runtime"`} {
		if !bytes.Contains(entries["doctor.json"], []byte(safe)) {
			t.Fatalf("doctor report lost safe diagnostic value %q:\n%s", safe, entries["doctor.json"])
		}
	}
	for _, raw := range []string{reportSecret, "home-REPORT-SEED", reportProject, reportSession, reportWindow, reportPane, reportThread, reportRouting, reportUUID, reportArgv, reportPrompt, reportEnv, reportSocket, reportName} {
		if bytes.Contains(preview, []byte(raw)) || bytes.Contains(archiveText, []byte(raw)) {
			t.Fatalf("raw sensitive value %q leaked\npreview=%s\narchive=%s", raw, preview, archiveText)
		}
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{operationsPath, ingestPath, configPath} {
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("report repaired source %q mode to %o", filepath.Base(path), info.Mode().Perm())
			}
		}
	}
}

func TestRedactDoctorJSONEnforcesStringArrayAllowlist(t *testing.T) {
	value := map[string]any{
		"safe_codes": []any{string(diagnostics.CodeTmuxApplyFailed), reportSecret},
	}
	redactDoctorJSON(value, "")
	got := value["safe_codes"].([]any)
	if got[0] != string(diagnostics.CodeTmuxApplyFailed) {
		t.Fatalf("allowed safe code changed: %#v", got)
	}
	if got[1] == reportSecret || !strings.HasPrefix(got[1].(string), "sha256:") {
		t.Fatalf("unlisted array scalar was not hashed: %#v", got)
	}
}

func TestSupportArchiveDeterministicFixture(t *testing.T) {
	cmd, stateHome, configHome := testReportCommand(t)
	seedReportSources(t, stateHome, configHome)
	now := time.Date(2026, 8, 13, 2, 3, 4, 0, time.UTC)
	root := t.TempDir()
	planA, err := cmd.buildSupportPlan(filepath.Join(root, "a.tar.gz"), now)
	if err != nil {
		t.Fatal(err)
	}
	planB, err := cmd.buildSupportPlan(filepath.Join(root, "b.tar.gz"), now)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSupportArchiveAtomic(planA); err != nil {
		t.Fatal(err)
	}
	if err := writeSupportArchiveAtomic(planB); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(planA.destination)
	b, _ := os.ReadFile(planB.destination)
	if !bytes.Equal(a, b) {
		t.Fatal("same report fixture produced non-deterministic archives")
	}
}

func TestSupportDoctorSafeAllowlistTracksProducerInventory(t *testing.T) {
	values := supportDoctorSafeStringValues()
	for _, provider := range aiprovider.HookDiagnosticSupported() {
		if !values["provider_id"][string(provider.ID)] || !values["agent"][string(provider.ID)] || !values["name"][provider.HookDiagnostics.Name] || !values["id"][provider.HookDiagnostics.ID] {
			t.Fatalf("safe Doctor allowlist does not track provider %#v", provider)
		}
	}
	for _, status := range []string{"available", "stale", "unavailable"} {
		if !values["status"][status] {
			t.Fatalf("safe Doctor allowlist missing resume status %q", status)
		}
	}
	for _, code := range doctorFindingCodeInventory {
		if !values["code"][code] {
			t.Fatalf("safe Doctor allowlist missing finding code %q", code)
		}
	}
	for _, remediation := range doctorFindingRemediationInventory {
		if !values["remediation"][remediation] {
			t.Fatalf("safe Doctor allowlist missing remediation %q", remediation)
		}
	}
}

func TestDiagnosticsReportSourceStatesAndCollision(t *testing.T) {
	cmd, stateHome, _ := testReportCommand(t)
	output := filepath.Join(t.TempDir(), "report.tar.gz")
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"report", "--output", output}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	entries, _ := readSupportArchive(t, output)
	var missing supportManifest
	_ = json.Unmarshal(entries["manifest.json"], &missing)
	assertManifestReason(t, missing, "operational-errors.json", "omitted", "source-missing")
	assertManifestReason(t, missing, "ai-ingest-summary.json", "omitted", "source-missing")

	original := []byte("existing must survive")
	if err := os.WriteFile(output, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run([]string{"report", "--output", output}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || strings.Contains(err.Error(), output) {
		t.Fatalf("collision error = %v", err)
	}
	got, _ := os.ReadFile(output)
	if !bytes.Equal(got, original) {
		t.Fatalf("collision changed existing file to %q", got)
	}

	operationsPath := filepath.Join(stateHome, "projmux", "logs", diagnostics.LogFileName)
	if err := os.MkdirAll(filepath.Dir(operationsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(operationsPath, []byte("corrupt\n{\"truncated\""), 0o600); err != nil {
		t.Fatal(err)
	}
	corruptPlan, err := cmd.buildSupportPlan(filepath.Join(t.TempDir(), "corrupt.tar.gz"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertManifestReason(t, corruptPlan.manifest, "operational-errors.json", "omitted", "source-corrupt-no-valid-errors")
}

func TestDiagnosticsReportPermissionDeniedSourcesAreStableOmissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission denial fixture")
	}
	cmd, stateHome, _ := testReportCommand(t)
	operationsPath := filepath.Join(stateHome, "projmux", "logs", diagnostics.LogFileName)
	ingestPath := filepath.Join(stateHome, "projmux", aiIngestLogName)
	for _, path := range []string{operationsPath, ingestPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("private"), 0o000); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(path, 0o600)
	}
	plan, err := cmd.buildSupportPlan(filepath.Join(t.TempDir(), "permission.tar.gz"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertManifestReason(t, plan.manifest, "operational-errors.json", "omitted", "source-permission-denied")
	assertManifestReason(t, plan.manifest, "ai-ingest-summary.json", "omitted", "source-permission-denied")
	for _, path := range []string{operationsPath, ingestPath} {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0 {
			t.Fatalf("report repaired denied source %q to %o", filepath.Base(path), info.Mode().Perm())
		}
	}
}

func TestDiagnosticsReportCleanOperationsAndDeniedOutputParent(t *testing.T) {
	cmd, stateHome, _ := testReportCommand(t)
	operationsPath := filepath.Join(stateHome, "projmux", "logs", diagnostics.LogFileName)
	if err := os.MkdirAll(filepath.Dir(operationsPath), 0o700); err != nil {
		t.Fatal(err)
	}
	clean := diagnosticsFixture("clean-info", "info", "cli")
	if err := diagnostics.NewStore(operationsPath).Append(clean); err != nil {
		t.Fatal(err)
	}
	plan, err := cmd.buildSupportPlan(filepath.Join(t.TempDir(), "clean.tar.gz"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	assertManifestReason(t, plan.manifest, "operational-errors.json", "omitted", "source-clean-no-errors")

	if runtime.GOOS == "windows" {
		return
	}
	denied := filepath.Join(t.TempDir(), reportSecret)
	if err := os.Mkdir(denied, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(denied, 0o700)
	output := filepath.Join(denied, reportProject, "report.tar.gz")
	var preview bytes.Buffer
	err = cmd.Run([]string{"report", "--output", output}, &preview, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), denied) || strings.Contains(err.Error(), reportSecret) || strings.Contains(err.Error(), reportProject) {
		t.Fatalf("denied output error = %v", err)
	}
	if !strings.Contains(preview.String(), "projmux diagnostics report preview") {
		t.Fatalf("denied output did not complete preview first: %q", preview.String())
	}
	if _, err := os.Stat(filepath.Join(denied, reportProject)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("denied output created/damaged destination parent: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(denied, ".projmux-support-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("denied output left temp files: %q", matches)
	}
}

func assertManifestReason(t *testing.T, manifest supportManifest, name, status, reason string) {
	t.Helper()
	for _, entry := range manifest.Entries {
		if entry.Name == name {
			if entry.Status != status || entry.Reason != reason {
				t.Fatalf("manifest entry %q = %#v", name, entry)
			}
			return
		}
	}
	t.Fatalf("manifest entry %q missing", name)
}

func TestDiagnosticsReportWritesNothingUntilCompletePreview(t *testing.T) {
	cmd, _, _ := testReportCommand(t)
	parent := filepath.Join(t.TempDir(), reportSecret, "nested")
	output := filepath.Join(parent, "report.tar.gz")
	err := cmd.Run([]string{"report", "--output", output}, failingWriter{}, &bytes.Buffer{})
	if err == nil || strings.Contains(err.Error(), reportSecret) || strings.Contains(err.Error(), output) {
		t.Fatalf("preview error = %v", err)
	}
	if _, err := os.Stat(parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output parent exists before successful preview: %v", err)
	}
}

func TestSupportPathLabelNeverEchoesUserControlledExtension(t *testing.T) {
	for _, tc := range []struct {
		path       string
		wantSuffix string
	}{
		{path: "/tmp/report.tar.gz", wantSuffix: ".tar.gz"},
		{path: "/tmp/report.SYNTH-SECRET"},
		{path: "/tmp/report"},
	} {
		label := supportPathLabel(tc.path)
		if !strings.HasPrefix(label, "sha256:") || !strings.HasSuffix(label, tc.wantSuffix) || strings.Contains(label, "SYNTH-SECRET") {
			t.Fatalf("supportPathLabel(%q) = %q", tc.path, label)
		}
	}
	cmd, _, _ := testReportCommand(t)
	output := filepath.Join(t.TempDir(), "report.SYNTH-SECRET")
	var preview bytes.Buffer
	if err := cmd.Run([]string{"report", "--output", output}, &preview, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	entries, archive := readSupportArchive(t, output)
	archiveBeforeCollision, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	infoBeforeCollision, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	collisionErr := cmd.Run([]string{"report", "--output", output}, &bytes.Buffer{}, &bytes.Buffer{})
	if collisionErr == nil {
		t.Fatal("sensitive-extension collision unexpectedly succeeded")
	}
	archiveAfterCollision, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	infoAfterCollision, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(archiveAfterCollision, archiveBeforeCollision) || infoAfterCollision.Mode() != infoBeforeCollision.Mode() {
		t.Fatal("sensitive-extension collision changed the existing target")
	}
	if bytes.Contains(preview.Bytes(), []byte("SYNTH-SECRET")) || bytes.Contains(archive, []byte("SYNTH-SECRET")) || bytes.Contains(entries["manifest.json"], []byte("SYNTH-SECRET")) || strings.Contains(collisionErr.Error(), "SYNTH-SECRET") {
		t.Fatalf("sensitive destination extension leaked preview=%s archive=%s", preview.Bytes(), archive)
	}
}

func TestWriteSupportArchiveAtomicCleansPartialAndDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "report.tar.gz")
	plan := supportPlan{destination: output, entries: []supportArchiveEntry{{name: "bad\x00name", data: []byte("private")}}}
	if err := writeSupportArchiveAtomic(plan); err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("partial write error = %v", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial output stat = %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".projmux-support-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("partial temp files remain: %q", matches)
	}
}

func TestWriteSupportArchiveAtomicAllowsSymlinkedParentWithoutReplacingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX symlink and private-mode fixture")
	}
	root := t.TempDir()
	realParent := filepath.Join(root, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked-parent")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(linkedParent, "report.tar.gz")
	plan := supportPlan{destination: output, entries: []supportArchiveEntry{{name: "manifest.json", data: []byte("{}\n")}}}
	if err := writeSupportArchiveAtomic(plan); err != nil {
		t.Fatal(err)
	}
	realOutput := filepath.Join(realParent, "report.tar.gz")
	info, err := os.Stat(realOutput)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("symlink-parent output mode=%v err=%v", info.Mode().Perm(), err)
	}
	original, err := os.ReadFile(realOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSupportArchiveAtomic(plan); err == nil {
		t.Fatal("second symlink-parent publish replaced existing target")
	}
	got, err := os.ReadFile(realOutput)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("collision through symlink parent changed target: %q err=%v", got, err)
	}
	matches, _ := filepath.Glob(filepath.Join(realParent, ".projmux-support-*.tmp"))
	if len(matches) != 0 {
		t.Fatalf("symlink-parent publish left temp files: %q", matches)
	}
}

func TestDiagnosticsReportAllPathsBypassLegacyMigration(t *testing.T) {
	for _, args := range [][]string{
		{"diagnostics", "report"},
		{"diagnostics", "report", "--output", "/tmp/report.tar.gz"},
		{"diagnostics", "report", "--unknown"},
		{"diagnostics", "report", "extra"},
	} {
		if shouldRunLegacyHookMigrations(args) {
			t.Fatalf("shouldRunLegacyHookMigrations(%q) = true", args)
		}
	}
}
