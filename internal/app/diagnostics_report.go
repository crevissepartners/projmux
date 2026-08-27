package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/version"
)

const (
	supportReportSchemaVersion = 2
	supportRedactionMode       = "default-hash-v1"
	supportOperationalTail     = 50
)

type supportManifest struct {
	ReportSchemaVersion int                    `json:"report_schema_version"`
	RedactionMode       string                 `json:"redaction_mode"`
	CreatedAt           string                 `json:"created_at"`
	Entries             []supportManifestEntry `json:"entries"`
}

type supportManifestEntry struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Reason      string `json:"reason"`
	RecordCount int    `json:"record_count,omitempty"`
}

type supportArchiveEntry struct {
	name string
	data []byte
}

type supportPlan struct {
	destination string
	manifest    supportManifest
	entries     []supportArchiveEntry
}

func (c *diagnosticsCommand) runReport(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("diagnostics report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("output", "", "local support archive destination")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError("diagnostics report does not accept positional arguments")
	}

	now := time.Now().UTC()
	destination, err := supportReportDestination(strings.TrimSpace(*output), now)
	if err != nil {
		return errors.New("resolve support report destination failed")
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("support report destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect support report destination failed")
	}

	plan, err := c.buildSupportPlan(destination, now)
	if err != nil {
		return errors.New("build support report plan failed")
	}
	preview := formatSupportPreview(plan)
	// The preview is the explicit synchronous contract. No parent, temporary,
	// or archive write may happen until the complete preview has been accepted
	// by the caller's writer.
	if _, err := io.WriteString(stdout, preview); err != nil {
		return errors.New("write support report preview failed")
	}
	if err := writeSupportArchiveAtomic(plan); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "created: %s\n", supportPathLabel(destination)); err != nil {
		return errors.New("write support report completion failed")
	}
	return nil
}

func supportReportDestination(requested string, now time.Time) (string, error) {
	if requested != "" {
		return filepath.Abs(requested)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve support report working directory: %w", err)
	}
	name := "projmux-support-report-" + now.UTC().Format("20060102T150405Z") + ".tar.gz"
	return filepath.Join(cwd, name), nil
}

func (c *diagnosticsCommand) buildSupportPlan(destination string, now time.Time) (supportPlan, error) {
	manifest := supportManifest{
		ReportSchemaVersion: supportReportSchemaVersion,
		RedactionMode:       supportRedactionMode,
		CreatedAt:           now.UTC().Format(time.RFC3339),
		Entries: []supportManifestEntry{
			{Name: "manifest.json", Status: "included", Reason: "report-contract"},
		},
	}
	entries := make([]supportArchiveEntry, 0, 6)

	systemData, err := supportJSON(map[string]any{
		"projmux_version":  version.String(),
		"platform":         runtime.GOOS + "/" + runtime.GOARCH,
		"selected_backend": diagnostics.MuxBackend(),
	})
	if err != nil {
		return supportPlan{}, err
	}
	entries = append(entries, supportArchiveEntry{name: "system.json", data: systemData})
	manifest.Entries = append(manifest.Entries, supportManifestEntry{Name: "system.json", Status: "included", Reason: "safe-static-fields"})

	doctorData, err := c.supportDoctorJSON()
	if err != nil {
		return supportPlan{}, fmt.Errorf("build versioned doctor report: %w", err)
	}
	entries = append(entries, supportArchiveEntry{name: "doctor.json", data: doctorData})
	manifest.Entries = append(manifest.Entries, supportManifestEntry{Name: "doctor.json", Status: "included", Reason: "doctor-schema-version-2"})

	configData, configManifest, err := c.supportConfigPresence()
	if err != nil {
		return supportPlan{}, err
	}
	entries = append(entries, supportArchiveEntry{name: "config-presence.json", data: configData})
	manifest.Entries = append(manifest.Entries, configManifest)

	operationsData, operationsManifest, err := c.supportOperationalErrors()
	if err != nil {
		return supportPlan{}, err
	}
	manifest.Entries = append(manifest.Entries, operationsManifest)
	if operationsManifest.Status == "included" {
		entries = append(entries, supportArchiveEntry{name: operationsManifest.Name, data: operationsData})
	}

	aiData, aiManifest := c.supportAIIngestSummary()
	manifest.Entries = append(manifest.Entries, aiManifest)
	if aiManifest.Status == "included" {
		entries = append(entries, supportArchiveEntry{name: aiManifest.Name, data: aiData})
	}

	manifestData, err := supportJSON(manifest)
	if err != nil {
		return supportPlan{}, err
	}
	entries = append([]supportArchiveEntry{{name: "manifest.json", data: manifestData}}, entries...)
	return supportPlan{destination: destination, manifest: manifest, entries: entries}, nil
}

func (c *diagnosticsCommand) supportDoctorJSON() ([]byte, error) {
	doctor := c.doctor
	if doctor == nil {
		doctor = newDoctorCommand()
	}
	report := doctor.evaluateReportForTrigger(doctorSectionAll, codexappserver.TriggerSupportReport)
	var raw bytes.Buffer
	if err := writeDoctorJSON(&raw, report, doctorSectionAll); err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(raw.Bytes(), &value); err != nil {
		return nil, err
	}
	redactDoctorJSON(value, "")
	return supportJSON(value)
}

var doctorSafeStringValues = supportDoctorSafeStringValues()

func supportDoctorSafeStringValues() map[string]map[string]bool {
	values := map[string]map[string]bool{
		"status": {
			string(doctorStatusOK): true, string(doctorStatusMissing): true, string(doctorStatusStale): true,
			string(doctorStatusHint): true, string(doctorStatusSkip): true,
			string(doctorAINotifyStatusInstalled): true, string(doctorAINotifyStatusConflict): true,
			"available": true, "unavailable": true,
		},
		"source":             {"app-server": true, "hook-fallback": true, "unavailable": true},
		"availability":       {"available": true, "unsupported": true, "unavailable": true, "timeout": true, "protocol-error": true},
		"reason":             {"none": true, "executable-missing": true, "daemon-not-running": true, "endpoint-unavailable": true, "unsupported": true, "timeout": true, "protocol-error": true, "disconnected": true, "hook-unavailable": true},
		"probe_reason":       {"none": true, "executable-missing": true, "daemon-not-running": true, "endpoint-unavailable": true, "unsupported": true, "timeout": true, "protocol-error": true, "disconnected": true},
		"install_capability": {"managed-ready": true, "external-cli-only": true, "cli-missing": true, "unknown": true},
		"endpoint_kind":      {"stdio": true, "stdio-proxy": true},
		"connection_state":   {"disconnected": true, "connecting": true, "ready": true, "timed-out": true, "protocol-error": true},
		"lifecycle_outcome":  {"already-running": true, "started": true, "start-failed": true, "not-attempted": true},
		"lifecycle_reason": {
			"read-only": true, "already-ready": true, "ready-after-start": true,
			"probe-executable-missing": true, "probe-timeout": true, "probe-unsupported": true,
			"probe-protocol-error": true, "probe-endpoint-error": true, "probe-unavailable": true,
			"start-executable-missing": true, "start-managed-payload-missing": true,
			"start-nonzero": true, "start-timeout": true,
			"readiness-executable-missing": true, "readiness-socket-unavailable": true,
			"readiness-timeout": true, "readiness-unsupported": true,
			"readiness-protocol-error": true, "readiness-endpoint-error": true,
		},
		"provider_id": {},
		"agent":       {},
		"confidence":  {"high": true, "medium": true, "low": true, "none": true},
		"name":        {"tmux": true, "git": true, "stty": true, "tmux bell fallback": true},
		"id":          {"tmux-bell": true},
		"severity":    {string(doctorSeverityInfo): true, string(doctorSeverityWarning): true, string(doctorSeverityError): true},
		"code":        {},
		"remediation": {},
		"safe_codes":  {},
		"divergence":  {},
	}
	for _, divergence := range resourcegraph.Divergences() {
		values["divergence"][string(divergence)] = true
	}
	for _, code := range doctorFindingCodeInventory {
		values["code"][code] = true
	}
	for _, remediation := range doctorFindingRemediationInventory {
		values["remediation"][remediation] = true
	}
	for _, code := range diagnostics.AllowedCodes() {
		values["safe_codes"][string(code)] = true
	}
	for _, provider := range aiprovider.HookDiagnosticSupported() {
		values["provider_id"][string(provider.ID)] = true
		values["agent"][string(provider.ID)] = true
		values["name"][provider.HookDiagnostics.Name] = true
		values["id"][provider.HookDiagnostics.ID] = true
	}
	return values
}

func redactDoctorJSON(value any, key string) {
	switch typed := value.(type) {
	case map[string]any:
		for childKey, child := range typed {
			if (childKey == "window_index" || childKey == "pane_index") && child != nil {
				typed[childKey] = supportHash(childKey, fmt.Sprint(child))
				continue
			}
			if text, ok := child.(string); ok && text != "" {
				if !doctorSafeStringValues[childKey][text] && !(key == "codex_app_server" && childKey == "version" && codexappserver.IsSafeDiagnosticVersion(text)) {
					typed[childKey] = supportHash(childKey, text)
				}
			} else {
				redactDoctorJSON(child, childKey)
			}
		}
	case []any:
		for index, child := range typed {
			if text, ok := child.(string); ok && text != "" {
				if !doctorSafeStringValues[key][text] {
					typed[index] = supportHash(key, text)
				}
				continue
			}
			redactDoctorJSON(child, key)
		}
	}
}

func (c *diagnosticsCommand) supportConfigPresence() ([]byte, supportManifestEntry, error) {
	homeDir := c.homeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	home, err := homeDir()
	if err != nil {
		return nil, supportManifestEntry{}, fmt.Errorf("resolve config presence home: %w", err)
	}
	lookupEnv := c.lookupEnv
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	paths, err := (config.Homes{HomeDir: home, ConfigHome: lookupEnv("XDG_CONFIG_HOME"), StateHome: lookupEnv("XDG_STATE_HOME")}).Paths()
	if err != nil {
		return nil, supportManifestEntry{}, err
	}
	type presence struct {
		Source string `json:"source"`
		State  string `json:"state"`
	}
	items := []presence{{Source: "global", State: supportPresenceState(paths.GlobalConfigFile())}}
	if cwd := strings.TrimSpace(lookupEnv("PROJMUX_CWD")); cwd != "" {
		items = append(items, presence{Source: "project", State: supportPresenceState(filepath.Join(cwd, ".projmux", config.GlobalConfigFileName))})
	} else {
		items = append(items, presence{Source: "project", State: "not-configured"})
	}
	data, err := supportJSON(map[string]any{"configs": items})
	return data, supportManifestEntry{Name: "config-presence.json", Status: "included", Reason: "presence-only-no-values", RecordCount: len(items)}, err
}

func supportPresenceState(path string) string {
	info, err := os.Lstat(path)
	switch {
	case err == nil && info.Mode().IsRegular():
		return "present"
	case err == nil:
		return "non-regular"
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	case errors.Is(err, os.ErrPermission):
		return "permission-denied"
	default:
		return "unreadable"
	}
}

func (c *diagnosticsCommand) supportOperationalErrors() ([]byte, supportManifestEntry, error) {
	path, err := diagnostics.DefaultPath(c.lookupEnv, c.homeDir)
	if err != nil {
		return nil, supportManifestEntry{}, err
	}
	result, err := diagnostics.NewStore(path).ReadOnly()
	entry := supportManifestEntry{Name: "operational-errors.json"}
	if err != nil {
		entry.Status = "omitted"
		entry.Reason = supportReadReason(err)
		return nil, entry, nil
	}
	if result.Missing {
		entry.Status, entry.Reason = "omitted", "source-missing"
		return nil, entry, nil
	}
	errorsOnly := make([]diagnostics.Event, 0, len(result.Events))
	for _, event := range result.Events {
		if event.Level != "error" {
			continue
		}
		event.RunID = supportHash("run", event.RunID)
		event.Version = supportHash("version", event.Version)
		if event.Message != "" {
			event.Message = supportHash("message", event.Message)
		}
		errorsOnly = append(errorsOnly, event)
	}
	if len(errorsOnly) > supportOperationalTail {
		errorsOnly = errorsOnly[len(errorsOnly)-supportOperationalTail:]
	}
	if len(errorsOnly) == 0 {
		entry.Status = "omitted"
		switch {
		case result.Malformed > 0 || result.Truncated:
			entry.Reason = "source-corrupt-no-valid-errors"
		default:
			entry.Reason = "source-clean-no-errors"
		}
		return nil, entry, nil
	}
	entry.Status, entry.Reason, entry.RecordCount = "included", "recent-bounded-errors", len(errorsOnly)
	if result.Malformed > 0 || result.Truncated {
		entry.Reason = "recent-bounded-errors-corrupt-records-skipped"
	}
	data, err := supportJSON(map[string]any{"events": errorsOnly})
	return data, entry, err
}

type supportAISummaryRow struct {
	Source string `json:"source"`
	Result string `json:"result"`
	Count  int    `json:"count"`
}

func (c *diagnosticsCommand) supportAIIngestSummary() ([]byte, supportManifestEntry) {
	entry := supportManifestEntry{Name: "ai-ingest-summary.json"}
	ai := newAICommand()
	ai.lookupEnv = c.lookupEnv
	ai.homeDir = c.homeDir
	path, err := ai.aiIngestLogPath()
	if err != nil {
		entry.Status, entry.Reason = "omitted", "source-path-unavailable"
		return nil, entry
	}
	info, err := os.Lstat(path)
	if err != nil {
		entry.Status, entry.Reason = "omitted", supportReadReason(err)
		return nil, entry
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		entry.Status, entry.Reason = "omitted", "source-non-regular"
		return nil, entry
	}
	// #nosec G304 -- path is the fixed ai-ingest.log child of the resolved
	// projmux state directory; Lstat above rejects symlinks/non-regular files,
	// and no config, log, or payload value controls this filename.
	file, err := os.Open(path)
	if err != nil {
		entry.Status, entry.Reason = "omitted", supportReadReason(err)
		return nil, entry
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, aiIngestLogMaxSize+1))
	if err != nil {
		entry.Status, entry.Reason = "omitted", supportReadReason(err)
		return nil, entry
	}
	if len(data) > aiIngestLogMaxSize {
		entry.Status, entry.Reason = "omitted", "source-exceeds-bound"
		return nil, entry
	}
	allowedSources := map[string]bool{"codex-hook": true, "claude-hook": true, "antigravity-hook": true, "tmux-bell": true}
	allowedResults := map[string]bool{"error": true, "ignored": true, "deduped": true, "notify": true, "state": true, "quiet": true}
	counts := map[string]int{}
	malformed := 0
	for _, line := range nonEmptyLines(string(data)) {
		var item aiIngestLogEntry
		if json.Unmarshal([]byte(line), &item) != nil || !allowedSources[item.Source] || !allowedResults[item.Result] {
			malformed++
			continue
		}
		counts[item.Source+"\x00"+item.Result]++
	}
	if len(counts) == 0 {
		entry.Status = "omitted"
		if malformed > 0 {
			entry.Reason = "source-corrupt-no-safe-records"
		} else {
			entry.Reason = "source-clean-no-records"
		}
		return nil, entry
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]supportAISummaryRow, 0, len(keys))
	for _, key := range keys {
		parts := strings.SplitN(key, "\x00", 2)
		rows = append(rows, supportAISummaryRow{Source: parts[0], Result: parts[1], Count: counts[key]})
	}
	entry.Status, entry.Reason, entry.RecordCount = "included", "safe-counts-only", len(rows)
	if malformed > 0 {
		entry.Reason = "safe-counts-only-corrupt-records-skipped"
	}
	encoded, encodeErr := supportJSON(map[string]any{"summary": rows})
	if encodeErr != nil {
		entry.Status, entry.Reason = "omitted", "summary-encode-failed"
		return nil, entry
	}
	return encoded, entry
}

func supportReadReason(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "source-missing"
	case errors.Is(err, os.ErrPermission):
		return "source-permission-denied"
	default:
		return "source-unreadable"
	}
}

func supportHash(kind, raw string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + raw))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func supportPathLabel(path string) string {
	ext := ""
	if strings.HasSuffix(path, ".tar.gz") {
		ext = ".tar.gz"
	}
	return supportHash("path", path) + ext
}

func formatSupportPreview(plan supportPlan) string {
	var out strings.Builder
	out.WriteString("projmux diagnostics report preview\n")
	fmt.Fprintf(&out, "destination: %s\n", supportPathLabel(plan.destination))
	fmt.Fprintf(&out, "report_schema_version: %d\n", plan.manifest.ReportSchemaVersion)
	fmt.Fprintf(&out, "redaction_mode: %s\n", plan.manifest.RedactionMode)
	out.WriteString("entries:\n")
	for _, entry := range plan.manifest.Entries {
		fmt.Fprintf(&out, "  - %s: %s (%s)\n", entry.Name, entry.Status, entry.Reason)
	}
	out.WriteString("archive write follows this complete preview for this explicit invocation only\n")
	return out.String()
}

func supportJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeSupportArchiveAtomic(plan supportPlan) error {
	parent := filepath.Dir(plan.destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return errors.New("create support report parent failed")
	}
	temp, err := os.CreateTemp(parent, ".projmux-support-*.tmp")
	if err != nil {
		return errors.New("create support report temporary archive failed")
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return errors.New("set private support report permissions failed")
	}

	gz := gzip.NewWriter(temp)
	gz.Header.ModTime = time.Time{}
	gz.Header.OS = 255
	tarWriter := tar.NewWriter(gz)
	for _, entry := range plan.entries {
		header := &tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.data)), ModTime: time.Time{}, Typeflag: tar.TypeReg}
		if err := tarWriter.WriteHeader(header); err != nil {
			_ = temp.Close()
			return errors.New("write support report entry header failed")
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
			_ = temp.Close()
			return errors.New("write support report entry failed")
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = temp.Close()
		return errors.New("close support report tar stream failed")
	}
	if err := gz.Close(); err != nil {
		_ = temp.Close()
		return errors.New("close support report gzip stream failed")
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return errors.New("sync support report archive failed")
	}
	if err := temp.Close(); err != nil {
		return errors.New("close support report archive failed")
	}
	// A same-directory hard link publishes the fully closed temp inode without
	// replacing an existing destination. This gives collision-safe atomic local
	// publication; the private temp name is removed immediately afterwards.
	if err := os.Link(tempPath, plan.destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("support report destination already exists")
		}
		return errors.New("publish support report archive failed")
	}
	return nil
}
