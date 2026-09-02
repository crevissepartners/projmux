package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

const (
	canaryTimeout = 4 * time.Minute
	maxTestOutput = 4 * 1024 * 1024
)

func main() {
	var primitive string
	var output string
	var aggregateDir string
	var preflight string
	var expectedVersion string
	flag.StringVar(&primitive, "primitive", "", "one installed qualification primitive")
	flag.StringVar(&output, "output", "", "typed JSON artifact path")
	flag.StringVar(&aggregateDir, "aggregate-dir", "", "directory containing matrix child artifacts")
	flag.StringVar(&preflight, "preflight", "success", "declared Codex installation step outcome")
	flag.StringVar(&expectedVersion, "expected-version", "", "declared Codex version for all tuple fields")
	flag.Parse()

	if output == "" || (primitive == "") == (aggregateDir == "") {
		fmt.Fprintln(os.Stderr, "qualification runner requires --output and exactly one of --primitive or --aggregate-dir")
		os.Exit(2)
	}
	var artifact codexinstalled.QualificationArtifact
	var runErr error
	if primitive != "" {
		artifact, runErr = runPrimitive(codexinstalled.QualificationPrimitive(primitive), preflight, expectedVersion)
	} else {
		artifact, runErr = aggregate(aggregateDir)
	}
	expected := make([]codexinstalled.QualificationPrimitive, 0, len(artifact.Results))
	for _, result := range artifact.Results {
		expected = append(expected, result.Primitive)
	}
	encoded, encodeErr := artifact.JSON(expected)
	if encodeErr != nil {
		fmt.Fprintln(os.Stderr, "qualification runner could not validate typed evidence")
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "qualification runner could not create the artifact directory")
		os.Exit(2)
	}
	if err := os.WriteFile(output, append(encoded, '\n'), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "qualification runner could not write typed evidence")
		os.Exit(2)
	}
	fmt.Println(string(encoded))
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "installed Codex qualification is not compatible; inspect the typed artifact")
		os.Exit(1)
	}
}

func runPrimitive(primitive codexinstalled.QualificationPrimitive, preflight, expectedVersion string) (codexinstalled.QualificationArtifact, error) {
	spec, ok := codexinstalled.QualificationSpecFor(primitive)
	if !ok {
		return codexinstalled.QualificationArtifact{}, fmt.Errorf("unknown qualification primitive")
	}
	if preflight != "success" {
		result := codexinstalled.InstallationFailureQualification(spec)
		return childArtifact(result, capabilityLedgerForFailure(spec, result.Versions,
			codexinstalled.CapabilityReasonEndpointUnavailable)), fmt.Errorf("declared Codex installation did not succeed")
	}
	root, err := os.MkdirTemp("", "projmux-installed-qualification-")
	if err != nil {
		result := codexinstalled.ReduceQualification(spec, nil, false)
		return childArtifact(result, capabilityLedgerForFailure(spec, result.Versions,
			codexinstalled.CapabilityReasonEndpointUnavailable)), err
	}

	command := exec.Command("go", "test", "-count=1", "-timeout=3m", "-json", // #nosec G204 -- fixed command and canonical test spec.
		"./internal/integrations/agents/codexappserver", "-run", "^"+spec.TestName+"$")
	command.Env = qualificationEnvironment(root, spec.SmokeEnv)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output boundedOutput
	command.Stdout = &output
	command.Stderr = &output

	succeeded := false
	if err := command.Start(); err == nil {
		exited := make(chan error, 1)
		go func() { exited <- command.Wait() }()
		timer := time.NewTimer(canaryTimeout)
		select {
		case waitErr := <-exited:
			succeeded = waitErr == nil
		case <-timer.C:
			retireProcessGroup(command.Process.Pid)
			<-exited
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		retireProcessGroup(command.Process.Pid)
	}
	observed := decodeInstalledResults(output.Bytes())
	capabilities := decodeInstalledCapabilities(output.Bytes())
	if output.truncated {
		observed = append(observed, codexinstalled.Result{})
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil || len(entries) != 0 {
		succeeded = false
		observed = append(observed, codexinstalled.Result{})
	}
	if err := codexinstalled.CleanupQualificationRoot(root); err != nil {
		succeeded = false
		observed = append(observed, codexinstalled.Result{})
	}

	result := codexinstalled.ReduceQualification(spec, observed, succeeded)
	result = codexinstalled.EnforceQualificationVersion(result, expectedVersion)
	ledger, capabilityErr := reduceCapabilityLedger(spec, capabilities, result.Versions, expectedVersion)
	artifact := childArtifact(result, ledger)
	if err := result.Validate(); err != nil {
		return artifact, err
	}
	if result.Class != codexinstalled.ResultPass {
		return artifact, fmt.Errorf("qualification primitive is %s", result.Class)
	}
	if capabilityErr != nil {
		return artifact, capabilityErr
	}
	return artifact, nil
}

func childArtifact(result codexinstalled.QualificationResult, ledger *codexinstalled.CapabilityLedger) codexinstalled.QualificationArtifact {
	return codexinstalled.QualificationArtifact{
		SchemaVersion:    codexinstalled.QualificationSchemaVersion,
		Results:          []codexinstalled.QualificationResult{result},
		CapabilityLedger: ledger,
	}
}

func capabilityLedgerForFailure(
	spec codexinstalled.QualificationSpec,
	versions codexinstalled.VersionTuple,
	reason codexinstalled.CapabilityReason,
) *codexinstalled.CapabilityLedger {
	if spec.Primitive != codexinstalled.PrimitivePreTurnAttach {
		return nil
	}
	result := codexinstalled.InfraErrorCapability(versions, reason, capabilityObservation(spec))
	return &codexinstalled.CapabilityLedger{
		SchemaVersion: codexinstalled.CapabilitySchemaVersion,
		Capabilities:  []codexinstalled.CapabilityResult{result},
	}
}

func reduceCapabilityLedger(
	spec codexinstalled.QualificationSpec,
	observed []codexinstalled.CapabilityResult,
	versions codexinstalled.VersionTuple,
	expectedVersion string,
) (*codexinstalled.CapabilityLedger, error) {
	if spec.Primitive != codexinstalled.PrimitivePreTurnAttach {
		if len(observed) != 0 {
			return nil, fmt.Errorf("non-capability primitive emitted capability evidence")
		}
		return nil, nil
	}
	var result codexinstalled.CapabilityResult
	var reduceErr error
	switch len(observed) {
	case 0:
		result = codexinstalled.InfraErrorCapability(versions, codexinstalled.CapabilityReasonTerminalMissing, capabilityObservation(spec))
		reduceErr = fmt.Errorf("pre-turn capability result is missing")
	case 1:
		result = observed[0]
		if err := result.Validate(); err != nil {
			result = codexinstalled.InfraErrorCapability(versions, codexinstalled.CapabilityReasonTerminalInvalid, capabilityObservation(spec))
			reduceErr = err
		} else {
			result = codexinstalled.EnforceCapabilityVersion(result, expectedVersion)
			if result.Result == codexinstalled.CapabilityInfraError {
				reduceErr = fmt.Errorf("pre-turn capability is infrastructure-error")
			}
		}
	default:
		result = codexinstalled.InfraErrorCapability(versions, codexinstalled.CapabilityReasonTerminalInvalid, capabilityObservation(spec))
		reduceErr = fmt.Errorf("pre-turn capability emitted %d terminal results", len(observed))
	}
	ledger := &codexinstalled.CapabilityLedger{
		SchemaVersion: codexinstalled.CapabilitySchemaVersion,
		Capabilities:  []codexinstalled.CapabilityResult{result},
	}
	return ledger, reduceErr
}

func capabilityObservation(spec codexinstalled.QualificationSpec) codexinstalled.CapabilityObservation {
	return codexinstalled.CapabilityObservation{
		Probe: spec.TestName,
		Run:   os.Getenv("PROJMUX_CODEX_EVIDENCE_RUN"),
	}
}

func qualificationEnvironment(root, smokeEnv string) []string {
	blocked := make(map[string]struct{})
	for _, spec := range codexinstalled.QualificationSpecs() {
		blocked[spec.SmokeEnv] = struct{}{}
	}
	blocked["CODEX_HOME"] = struct{}{}
	blocked["TMUX"] = struct{}{}
	blocked["TMUX_PANE"] = struct{}{}

	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, excluded := blocked[key]; excluded || credentialEnvironmentKey(key) {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, smokeEnv+"="+root)
}

func credentialEnvironmentKey(key string) bool {
	upper := strings.ToUpper(key)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "API_KEY"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func retireProcessGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(100 * time.Millisecond)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

type testEvent struct {
	Output string `json:"Output"`
}

func decodeInstalledResults(encoded []byte) []codexinstalled.Result {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	results := make([]codexinstalled.Result, 0)
	const marker = "installed-result: "
	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			break
		}
		index := strings.Index(event.Output, marker)
		if index < 0 {
			continue
		}
		var result codexinstalled.Result
		if err := json.Unmarshal([]byte(strings.TrimSpace(event.Output[index+len(marker):])), &result); err != nil {
			results = append(results, codexinstalled.Result{})
			continue
		}
		results = append(results, result)
	}
	return results
}

func decodeInstalledCapabilities(encoded []byte) []codexinstalled.CapabilityResult {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	results := make([]codexinstalled.CapabilityResult, 0)
	const marker = "installed-capability: "
	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			break
		}
		index := strings.Index(event.Output, marker)
		if index < 0 {
			continue
		}
		var result codexinstalled.CapabilityResult
		if err := json.Unmarshal([]byte(strings.TrimSpace(event.Output[index+len(marker):])), &result); err != nil {
			results = append(results, codexinstalled.CapabilityResult{})
			continue
		}
		results = append(results, result)
	}
	return results
}

func aggregate(directory string) (codexinstalled.QualificationArtifact, error) {
	children := make(map[codexinstalled.QualificationPrimitive][]byte)
	unexpected := false
	entries, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		unexpected = true
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			unexpected = true
			continue
		}
		primitive := codexinstalled.QualificationPrimitive(strings.TrimSuffix(entry.Name(), ".json"))
		if _, ok := codexinstalled.QualificationSpecFor(primitive); !ok {
			unexpected = true
			continue
		}
		encoded, readErr := os.ReadFile(filepath.Join(directory, entry.Name())) // #nosec G304 -- exact entry below the artifact input directory.
		if readErr != nil {
			unexpected = true
			continue
		}
		children[primitive] = encoded
	}
	artifact, aggregateErr := codexinstalled.AggregateQualificationArtifacts(children)
	if unexpected {
		aggregateErr = errors.Join(aggregateErr, fmt.Errorf("qualification input contains unexpected entries"))
	}
	return artifact, aggregateErr
}

type boundedOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	original := len(data)
	remaining := maxTestOutput - output.buffer.Len()
	if len(data) > remaining {
		data = data[:max(remaining, 0)]
		output.truncated = true
	}
	_, _ = output.buffer.Write(data)
	return original, nil
}

func (output *boundedOutput) Bytes() []byte { return output.buffer.Bytes() }
