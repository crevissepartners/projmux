package app

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Codex owns hook trust: it writes a `[hooks.state]` subtable holding the
// `trusted_hash` the user approved for each reviewed hook, into the same
// config.toml projmux converges. These tests pin the ownership split — projmux
// converges only the hook definitions it authored and never reads, writes,
// moves, or synthesizes a trust hash.

const codexTrustUnrelatedTables = `[tui]
theme = "dark"

[projects."/home/u/repo"]
trust_level = "trusted"
`

// codexTrustStateTables renders the exact `[hooks.state]` shape Codex writes.
// The hashes are fixture bytes; nothing in projmux may produce or reuse them.
func codexTrustStateTables(scope string, events []string) string {
	var out strings.Builder
	for i, event := range events {
		out.WriteString("[hooks.state.\"" + scope + ":" + event + ":0:0\"]\n")
		out.WriteString("trusted_hash = \"sha256:" + strings.Repeat(string(rune('a'+i%26)), 64) + "\"\n\n")
	}
	return out.String()
}

func codexTrustStateHeader() string { return "[hooks.state]\n\n" }

// codexConfigFixture joins a prologue and the managed block with the exact
// separator appendCodexHooksBlock produces, so a converged fixture really is
// byte-identical to what convergence would write.
func codexConfigFixture(prologue, block string) string {
	return strings.TrimRight(prologue, "\r\n") + "\n\n" + block
}

func codexTrustStateEvents() []string {
	return []string{
		"pre_tool_use", "permission_request", "post_tool_use", "pre_compact",
		"post_compact", "session_start", "user_prompt_submit", "stop",
	}
}

// codexTrustEntries returns every `[hooks.state."…"]` key with the hash it
// holds, so a test can assert exact preservation instead of a mere count.
func codexTrustEntries(t *testing.T, config string) map[string]string {
	t.Helper()
	entries := map[string]string{}
	key := ""
	for line := range strings.SplitSeq(config, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, `[hooks.state."`) && strings.HasSuffix(trimmed, `"]`):
			key = trimmed
		case strings.HasPrefix(trimmed, "["):
			key = ""
		case strings.HasPrefix(trimmed, "trusted_hash"):
			if key == "" {
				t.Fatalf("trusted_hash outside a [hooks.state] table: %q", trimmed)
			}
			entries[key] = trimmed
		}
	}
	return entries
}

func codexHookDefinitionLines(t *testing.T, config string) []string {
	t.Helper()
	lines := []string{}
	for line := range strings.SplitSeq(config, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[[hooks.") || strings.HasPrefix(trimmed, "matcher =") ||
			strings.HasPrefix(trimmed, "type =") || strings.HasPrefix(trimmed, "command =") {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func writeCodexHookCatalogOverride(t *testing.T, home, body string) {
	t.Helper()
	writeCodexTestFile(t, filepath.Join(home, ".config", "projmux", "ai-hooks.d", "codex.json"), body)
}

func runCodexIntegrate(t *testing.T, cmd *aiCommand, args ...string) string {
	t.Helper()
	var stdout bytes.Buffer
	if err := cmd.Run(append([]string{"integrate", "codex"}, args...), &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run integrate codex %v error = %v", args, err)
	}
	return stdout.String()
}

// codexManagedHooksBlockLines is the byte-exact managed block for the default
// catalog, used to build fixtures that already carry reviewed hook definitions.
func codexManagedHooksBlockLines() string {
	return codexHooksBlockForEvents(false, defaultAIHookInstallEvents(aiHookProviderCodex))
}

func codexUnmarkedHookWiring() string {
	block := codexManagedHooksBlockLines()
	block = strings.TrimPrefix(block, codexHooksMarkerBegin+"\n")
	return strings.TrimSuffix(block, codexHooksMarkerEnd+"\n")
}

// 수용 기준 1 / 6 — trust state Codex re-serialized into the middle of the
// managed marker block survives an explicit integration byte for byte.
func TestAIIntegrateCodexPreservesTrustStateInsideManagedBlock(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	inBlockState := codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents())
	block := strings.TrimSuffix(codexManagedHooksBlockLines(), codexHooksMarkerEnd+"\n") +
		inBlockState + codexHooksMarkerEnd + "\n"
	before := `model = "gpt-5.1-codex"

[features]
hooks = true

` + codexTrustUnrelatedTables + "\n" + codexTrustStateHeader() +
		codexTrustStateTables("/home/u/.codex/hooks.json", []string{"stop", "session_start"}) +
		"\n" + block
	writeCodexTestFile(t, path, before)

	wantTrust := codexTrustEntries(t, before)
	if len(wantTrust) != 10 {
		t.Fatalf("fixture trust entries = %d, want 10", len(wantTrust))
	}
	wantDefinitions := codexHookDefinitionLines(t, before)

	runCodexIntegrate(t, cmd)
	after := readCodexTestFile(t, path)

	gotTrust := codexTrustEntries(t, after)
	if len(gotTrust) != len(wantTrust) {
		t.Fatalf("trust entries after integration = %d, want %d:\n%s", len(gotTrust), len(wantTrust), after)
	}
	for key, hash := range wantTrust {
		if gotTrust[key] != hash {
			t.Fatalf("trust entry %s = %q, want %q:\n%s", key, gotTrust[key], hash, after)
		}
	}
	if got := codexHookDefinitionLines(t, after); strings.Join(got, "\n") != strings.Join(wantDefinitions, "\n") {
		t.Fatalf("hook definitions changed:\nbefore:\n%s\nafter:\n%s", strings.Join(wantDefinitions, "\n"), strings.Join(got, "\n"))
	}
	if !strings.Contains(after, `theme = "dark"`) || !strings.Contains(after, `trust_level = "trusted"`) {
		t.Fatalf("unrelated config was not preserved:\n%s", after)
	}
	if strings.Count(after, codexHooksMarkerBegin) != 1 || strings.Count(after, codexHooksMarkerEnd) != 1 {
		t.Fatalf("managed markers were not restored exactly once:\n%s", after)
	}

	stdout := runCodexIntegrate(t, cmd)
	if second := readCodexTestFile(t, path); second != after {
		t.Fatalf("repeat integration changed config:\nfirst:\n%s\nsecond:\n%s", after, second)
	}
	if !strings.Contains(stdout, "no changes") {
		t.Fatalf("repeat stdout = %q, want no changes", stdout)
	}
}

// 수용 기준 1 — an already converged config produces an empty dry-run diff and
// the dry-run itself never touches the file.
func TestAIIntegrateCodexDryRunOnConvergedTrustedConfigIsEmptyDiff(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	before := codexConfigFixture(`[features]
hooks = true

`+codexTrustStateHeader()+
		codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents()), codexManagedHooksBlockLines())
	writeCodexTestFile(t, path, before)

	stdout := runCodexIntegrate(t, cmd, "--dry-run")
	if !strings.Contains(stdout, "no changes") || strings.Contains(stdout, "would update config") {
		t.Fatalf("dry-run stdout = %q, want an empty diff", stdout)
	}
	if got := readCodexTestFile(t, path); got != before {
		t.Fatalf("dry-run wrote to the config:\n%s", got)
	}
}

// 수용 기준 2 — repeated automatic convergence keeps bytes, mode and trust.
func TestManagedIngestConvergenceRepeatsWithoutTouchingCodexTrustState(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	inBlockState := codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents())
	before := `[features]
hooks = true

` + codexTrustStateHeader() +
		strings.TrimSuffix(codexManagedHooksBlockLines(), codexHooksMarkerEnd+"\n") +
		inBlockState + codexHooksMarkerEnd + "\n"
	writeCodexTestFile(t, path, before)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	wantTrust := codexTrustEntries(t, before)

	count, _, err := cmd.beginManagedIngestProducerFileMigration()
	if err != nil {
		t.Fatalf("first convergence error = %v", err)
	}
	if count != 1 {
		t.Fatalf("first convergence count = %d, want 1", count)
	}
	first := readCodexTestFile(t, path)

	count, _, err = cmd.beginManagedIngestProducerFileMigration()
	if err != nil {
		t.Fatalf("second convergence error = %v", err)
	}
	if count != 0 {
		t.Fatalf("second convergence count = %d, want 0", count)
	}
	if second := readCodexTestFile(t, path); second != first {
		t.Fatalf("second convergence changed bytes:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
	}
	gotTrust := codexTrustEntries(t, first)
	for key, hash := range wantTrust {
		if gotTrust[key] != hash {
			t.Fatalf("trust entry %s = %q, want %q", key, gotTrust[key], hash)
		}
	}
	if len(gotTrust) != len(wantTrust) {
		t.Fatalf("trust entries = %d, want %d", len(gotTrust), len(wantTrust))
	}
}

// 수용 기준 4 — a changed hook identity is never handed the old trust and never
// gets a synthesized hash, while the untouched hooks keep theirs.
func TestAIIntegrateCodexChangedHookIdentityDoesNotTransferTrust(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	before := codexConfigFixture(`[features]
hooks = true

`+codexTrustStateHeader()+
		codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents()), codexManagedHooksBlockLines())
	writeCodexTestFile(t, path, before)
	wantTrust := codexTrustEntries(t, before)

	writeCodexHookCatalogOverride(t, home, `{"provider":"codex","events":[
		{"name":"Stop","install":false,"action":"notify"},
		{"name":"SessionEnd","install":true,"action":"notify"}
	]}`)

	runCodexIntegrate(t, cmd)
	after := readCodexTestFile(t, path)

	gotTrust := codexTrustEntries(t, after)
	if len(gotTrust) != len(wantTrust) {
		t.Fatalf("trust entries = %d, want %d (projmux must neither add nor drop entries):\n%s", len(gotTrust), len(wantTrust), after)
	}
	for key, hash := range wantTrust {
		if gotTrust[key] != hash {
			t.Fatalf("trust entry %s = %q, want the original %q:\n%s", key, gotTrust[key], hash, after)
		}
	}
	if strings.Contains(after, `session_end:0:0`) {
		t.Fatalf("projmux minted trust state for the new hook identity:\n%s", after)
	}
	if !strings.Contains(after, "[[hooks.SessionEnd]]") || strings.Contains(after, "[[hooks.Stop]]") {
		t.Fatalf("changed hook identity was not converged:\n%s", after)
	}
}

// 수용 기준 5 — removing a managed hook leaves its provider-owned state and any
// unknown state alone rather than reusing the identity as if it were trusted.
func TestAIIntegrateCodexRemovedHookKeepsUnknownAndUnrelatedTrustState(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	before := codexConfigFixture(`[features]
hooks = true

`+codexTrustStateHeader()+
		codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents())+
		codexTrustStateTables("plugin@vendor:hooks/hooks.json", []string{"stop"})+
		`[hooks.state.experiment]
future_field = "unknown to projmux"
`, codexManagedHooksBlockLines())
	writeCodexTestFile(t, path, before)
	wantTrust := codexTrustEntries(t, before)

	writeCodexHookCatalogOverride(t, home, `{"provider":"codex","events":[
		{"name":"Stop","install":false,"action":"notify"}
	]}`)

	runCodexIntegrate(t, cmd)
	after := readCodexTestFile(t, path)

	if strings.Contains(after, "[[hooks.Stop]]") {
		t.Fatalf("removed hook definition survived:\n%s", after)
	}
	gotTrust := codexTrustEntries(t, after)
	for key, hash := range wantTrust {
		if gotTrust[key] != hash {
			t.Fatalf("trust entry %s = %q, want the original %q:\n%s", key, gotTrust[key], hash, after)
		}
	}
	if len(gotTrust) != len(wantTrust) {
		t.Fatalf("trust entries = %d, want %d:\n%s", len(gotTrust), len(wantTrust), after)
	}
	if !strings.Contains(after, `future_field = "unknown to projmux"`) {
		t.Fatalf("unknown provider state table was dropped:\n%s", after)
	}
}

// 수용 기준 6 — state and unknown tables are preserved wherever the provider
// left them relative to the managed markers.
func TestAIIntegrateCodexPreservesStateInEveryMarkerPlacement(t *testing.T) {
	state := codexTrustStateHeader() +
		codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents()) +
		`[unknown.future]
key = "value"

`
	block := codexManagedHooksBlockLines()
	inBlock := strings.TrimSuffix(block, codexHooksMarkerEnd+"\n") + state + codexHooksMarkerEnd + "\n"

	for _, test := range []struct {
		name   string
		config string
	}{
		{"before block", codexConfigFixture("[features]\nhooks = true\n\n"+state, block)},
		{"inside block", codexConfigFixture("[features]\nhooks = true", inBlock)},
		{"after block", "[features]\nhooks = true\n\n" + block + "\n" + state},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.readFile = os.ReadFile
			path := filepath.Join(home, codexConfigRelativePath)
			writeCodexTestFile(t, path, test.config)
			wantTrust := codexTrustEntries(t, test.config)

			runCodexIntegrate(t, cmd)
			after := readCodexTestFile(t, path)

			gotTrust := codexTrustEntries(t, after)
			if len(gotTrust) != len(wantTrust) {
				t.Fatalf("trust entries = %d, want %d:\n%s", len(gotTrust), len(wantTrust), after)
			}
			for key, hash := range wantTrust {
				if gotTrust[key] != hash {
					t.Fatalf("trust entry %s = %q, want %q:\n%s", key, gotTrust[key], hash, after)
				}
			}
			if !strings.Contains(after, `key = "value"`) || !strings.Contains(after, "[unknown.future]") {
				t.Fatalf("unknown table was dropped:\n%s", after)
			}
			runCodexIntegrate(t, cmd)
			if second := readCodexTestFile(t, path); second != after {
				t.Fatalf("repeat integration changed config:\nfirst:\n%s\nsecond:\n%s", after, second)
			}
		})
	}
}

// 수용 기준 9 — a config whose markers a Codex re-serialization dropped is
// recognized as projmux-owned again and converges instead of refusing.
func TestAIIntegrateCodexRecoversUnmarkedProjmuxHookWiring(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	before := codexConfigFixture(`[features]
hooks = true

`+codexTrustStateHeader()+
		codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents()), codexUnmarkedHookWiring())
	writeCodexTestFile(t, path, before)
	if strings.Contains(before, codexHooksMarkerBegin) {
		t.Fatal("fixture must not contain managed markers")
	}
	wantTrust := codexTrustEntries(t, before)
	wantDefinitions := codexHookDefinitionLines(t, before)

	dryRun := runCodexIntegrate(t, cmd, "--dry-run")
	if !strings.Contains(dryRun, "recovery: adopting projmux-authored Codex hook wiring") {
		t.Fatalf("dry-run stdout = %q, want the recovery notice", dryRun)
	}
	if strings.Contains(dryRun, "would refuse") {
		t.Fatalf("dry-run still refuses projmux-owned wiring: %q", dryRun)
	}

	runCodexIntegrate(t, cmd)
	after := readCodexTestFile(t, path)

	if strings.Count(after, codexHooksMarkerBegin) != 1 || strings.Count(after, codexHooksMarkerEnd) != 1 {
		t.Fatalf("markers were not restored exactly once:\n%s", after)
	}
	if got := codexHookDefinitionLines(t, after); strings.Join(got, "\n") != strings.Join(wantDefinitions, "\n") {
		t.Fatalf("recovery rewrote hook definitions, which would expire their trust:\nbefore:\n%s\nafter:\n%s",
			strings.Join(wantDefinitions, "\n"), strings.Join(got, "\n"))
	}
	gotTrust := codexTrustEntries(t, after)
	for key, hash := range wantTrust {
		if gotTrust[key] != hash {
			t.Fatalf("trust entry %s = %q, want %q:\n%s", key, gotTrust[key], hash, after)
		}
	}
	if len(gotTrust) != len(wantTrust) {
		t.Fatalf("trust entries = %d, want %d:\n%s", len(gotTrust), len(wantTrust), after)
	}
	runCodexIntegrate(t, cmd)
	if second := readCodexTestFile(t, path); second != after {
		t.Fatalf("repeat integration changed config:\nfirst:\n%s\nsecond:\n%s", after, second)
	}
}

// 수용 기준 9 — the same recovery reaches `config apply` and `make install`
// through the automatic producer-file convergence path.
func TestManagedIngestConvergenceRecoversUnmarkedCodexHookWiring(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	before := codexConfigFixture(`[features]
hooks = true

`+codexTrustStateHeader()+
		codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents()), codexUnmarkedHookWiring())
	writeCodexTestFile(t, path, before)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	wantTrust := codexTrustEntries(t, before)

	count, _, err := cmd.beginManagedIngestProducerFileMigration()
	if err != nil {
		t.Fatalf("convergence error = %v, want the unmarked wiring adopted", err)
	}
	if count != 1 {
		t.Fatalf("convergence count = %d, want 1", count)
	}
	after := readCodexTestFile(t, path)
	if !strings.Contains(after, codexHooksMarkerBegin) {
		t.Fatalf("convergence did not restore the managed markers:\n%s", after)
	}
	gotTrust := codexTrustEntries(t, after)
	if len(gotTrust) != len(wantTrust) {
		t.Fatalf("trust entries = %d, want %d:\n%s", len(gotTrust), len(wantTrust), after)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", info.Mode().Perm())
	}

	count, _, err = cmd.beginManagedIngestProducerFileMigration()
	if err != nil {
		t.Fatalf("repeat convergence error = %v", err)
	}
	if count != 0 {
		t.Fatalf("repeat convergence count = %d, want 0", count)
	}
	if second := readCodexTestFile(t, path); second != after {
		t.Fatalf("repeat convergence changed bytes:\nfirst:\n%s\nsecond:\n%s", after, second)
	}
}

// 수용 기준 9 — recovery is limited to wiring projmux itself authored. A hook a
// user hand-wrote around the ingest route keeps the pre-existing refusal, and
// neither the explicit nor the automatic path writes a byte.
func TestAIIntegrateCodexStillRefusesHandWrittenUnmanagedHooks(t *testing.T) {
	for _, test := range []struct {
		name string
		hook string
	}{
		{"custom command wrapper", `[[hooks.Stop]]
matcher = "*"
[[hooks.Stop.hooks]]
type = "command"
command = "logger mine && ` + canonicalCodexHookRoute + `"
`},
		{"custom matcher", `[[hooks.Stop]]
matcher = "Bash"
[[hooks.Stop.hooks]]
type = "command"
command = "` + codexHookCommand + `"
`},
		{"extra key projmux does not author", `[[hooks.Stop]]
matcher = "*"
timeout_ms = 250
[[hooks.Stop.hooks]]
type = "command"
command = "` + codexHookCommand + `"
`},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			cmd := testAICommand(home)
			cmd.readFile = os.ReadFile
			path := filepath.Join(home, codexConfigRelativePath)
			before := "[features]\nhooks = true\n\n" + test.hook
			writeCodexTestFile(t, path, before)

			err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "already configured outside a projmux-managed block") {
				t.Fatalf("integrate error = %v, want the unmanaged-hooks refusal", err)
			}
			if _, _, err := cmd.beginManagedIngestProducerFileMigration(); err == nil ||
				!strings.Contains(err.Error(), "already configured outside a projmux-managed block") {
				t.Fatalf("convergence error = %v, want the unmanaged-hooks refusal", err)
			}
			if got := readCodexTestFile(t, path); got != before {
				t.Fatalf("refusal still wrote to the config:\n%s", got)
			}
		})
	}
}

// Codex keys trust state by array position, so adopting unmarked wiring must not
// renumber a surviving hook entry the user owns. That case keeps refusing rather
// than silently expiring a hook projmux does not own.
func TestAIIntegrateCodexRefusesUnmarkedRecoveryThatWouldRenumberHooks(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	before := `[features]
hooks = true

[[hooks.Stop]]
matcher = "*"
[[hooks.Stop.hooks]]
type = "command"
command = "` + codexHookCommand + `"

[[hooks.Stop]]
matcher = "*"
[[hooks.Stop.hooks]]
type = "command"
command = "echo mine"
`
	writeCodexTestFile(t, path, before)

	err := cmd.Run([]string{"integrate", "codex"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "already configured outside a projmux-managed block") {
		t.Fatalf("integrate error = %v, want the unmanaged-hooks refusal", err)
	}
	if got := readCodexTestFile(t, path); got != before {
		t.Fatalf("refusal still wrote to the config:\n%s", got)
	}
}

// `--remove` also releases wiring whose markers the provider dropped, and still
// leaves provider-owned trust state for Codex to reconcile.
func TestAIIntegrateCodexRemoveReleasesUnmarkedWiringAndKeepsTrustState(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.readFile = os.ReadFile
	path := filepath.Join(home, codexConfigRelativePath)

	before := codexConfigFixture(`[features]
hooks = true

`+codexTrustStateHeader()+
		codexTrustStateTables("/home/u/.codex/config.toml", codexTrustStateEvents()), codexUnmarkedHookWiring())
	writeCodexTestFile(t, path, before)
	wantTrust := codexTrustEntries(t, before)

	stdout := runCodexIntegrate(t, cmd, "--remove")
	if !strings.Contains(stdout, "removed projmux-managed Codex hooks") {
		t.Fatalf("stdout = %q, want the removal message", stdout)
	}
	after := readCodexTestFile(t, path)
	if strings.Contains(after, codexHookCommand) {
		t.Fatalf("unmarked projmux wiring survived --remove:\n%s", after)
	}
	gotTrust := codexTrustEntries(t, after)
	if len(gotTrust) != len(wantTrust) {
		t.Fatalf("trust entries = %d, want %d:\n%s", len(gotTrust), len(wantTrust), after)
	}
	for key, hash := range wantTrust {
		if gotTrust[key] != hash {
			t.Fatalf("trust entry %s = %q, want %q:\n%s", key, gotTrust[key], hash, after)
		}
	}
}

// 수용 기준 7 — no production path names, computes, or writes a Codex trust
// hash. Trust stays a decision only the user and Codex can make.
func TestProductionSourceNeverProducesCodexTrustState(t *testing.T) {
	roots := []string{filepath.Join("..", "..", "internal"), filepath.Join("..", "..", "cmd")}
	offenders := []string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path) // #nosec G304 -- repository-relative source audit over the checked-in tree.
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(data), "\n") {
				code := strings.TrimSpace(stripCodexTomlComment(line))
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				for _, token := range []string{"trusted_hash", "hooks.state"} {
					if strings.Contains(code, token) {
						offenders = append(offenders, filepath.ToSlash(path)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(offenders)
	if len(offenders) != 0 {
		t.Fatalf("production source references Codex-owned trust state:\n%s", strings.Join(offenders, "\n"))
	}
}
