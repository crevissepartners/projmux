package app

import (
	"fmt"
	"os"
	"runtime"
	"runtime/metrics"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// The idle benchmark pads the fixture Registry to the resource shape of an
// operator Registry measured at ~107KB, so one Registry reload per tick costs
// what it costs on a real machine rather than on a one-Agent fixture.
const (
	claudeEndpointIdleProjects = 7
	claudeEndpointIdleWindows  = 8
	claudeEndpointIdlePanes    = 21
	claudeEndpointIdleAgents   = 38
)

// BenchmarkClaudeEndpointIdleTick measures the process CPU the real
// serveClaudeEndpoint loop spends while idle. One op is one
// claudeEndpointPollInterval of wall time with no lease probe connecting, so
// run it with an explicit count, e.g. -benchtime=200x for a 20s idle window.
func BenchmarkClaudeEndpointIdleTick(b *testing.B) {
	f := newClaudeEndpointTestFixture(b)
	padClaudeEndpointIdleRegistry(b, f.store)
	_, done := f.start(b)
	registryBytes := reportClaudeEndpointIdleRegistry(b, f)

	time.Sleep(3 * claudeEndpointPollInterval)
	runtime.GC()
	runtimeBefore := readClaudeEndpointIdleRuntime()
	cpuBefore := claudeEndpointProcessCPU(b)
	start := time.Now()
	b.ResetTimer()
	time.Sleep(time.Duration(b.N) * claudeEndpointPollInterval)
	b.StopTimer()
	wall := time.Since(start)
	cpu := claudeEndpointProcessCPU(b) - cpuBefore
	select {
	case <-done:
		b.Fatal("helper exited during the idle window; the measurement is invalid")
	default:
	}
	// The closing collection flushes allocation counters and lands the GC CPU
	// snapshot; its own cost is outside the rusage window but inside gc-cpu.
	runtime.GC()
	runtimeAfter := readClaudeEndpointIdleRuntime()

	ticks := float64(wall) / float64(claudeEndpointPollInterval)
	// ResetTimer drops user metrics, so every metric is reported after it.
	b.ReportMetric(float64(registryBytes), "registry-B")
	b.ReportMetric(0, "ns/op")
	b.ReportMetric(float64(cpu)/ticks, "cpu-ns/tick")
	b.ReportMetric(100*float64(cpu)/float64(wall), "%core")
	b.ReportMetric((runtimeAfter.allocBytes-runtimeBefore.allocBytes)/ticks, "alloc-B/tick")
	b.ReportMetric((runtimeAfter.gcCycles-runtimeBefore.gcCycles-1)*1000/ticks, "gc/1k-tick")
	b.ReportMetric((runtimeAfter.gcCPUSeconds-runtimeBefore.gcCPUSeconds)*1e9/ticks, "gc-cpu-ns/tick")
}

func padClaudeEndpointIdleRegistry(tb testing.TB, store *intmetadata.Store) {
	tb.Helper()
	mutator := intmetadata.DefaultMutator()
	mutator.DirExists = func(string) (bool, error) { return true, nil }
	if _, err := store.Update(func(reg *coremetadata.Registry) error {
		return seedClaudeEndpointIdleResources(mutator, reg)
	}); err != nil {
		tb.Fatal(err)
	}
}

// seedClaudeEndpointIdleResources adds unrelated Projects, Windows, running
// Agents, and mostly Offline/Failed Agents until the exact target counts hold.
func seedClaudeEndpointIdleResources(m coremetadata.Mutator, reg *coremetadata.Registry) error {
	var windows, roots []string
	for index := 0; len(reg.Projects) < claudeEndpointIdleProjects; index++ {
		root := fmt.Sprintf("/home/bench/source/repos/idle-project-%d", index)
		result, err := m.RegisterProject(reg, coremetadata.RegisterProjectOptions{Root: root, DefaultShell: "/bin/zsh",
			SessionName: fmt.Sprintf("idle-project-%d", index), OperationID: fmt.Sprintf("op-idle-project-%d", index)})
		if err != nil {
			return fmt.Errorf("seed project %d: %w", index, err)
		}
		windows, roots = append(windows, result.Windows[0].Metadata.UID), append(roots, root)
	}
	for index := 0; len(reg.Windows) < claudeEndpointIdleWindows; index++ {
		project, ok := reg.ProjectByRoot(roots[index%len(roots)])
		if !ok {
			return fmt.Errorf("seed window %d: project root disappeared", index)
		}
		window, _, err := m.AddWindow(reg, project.Metadata.UID, coremetadata.BootstrapWindow{}, "", fmt.Sprintf("op-idle-window-%d", index))
		if err != nil {
			return fmt.Errorf("seed window %d: %w", index, err)
		}
		windows, roots = append(windows, window.Metadata.UID), append(roots, project.Spec.Root)
	}
	for index := 0; len(reg.Agents) < claudeEndpointIdleAgents; index++ {
		running := len(reg.Panes) < claudeEndpointIdlePanes
		if err := seedClaudeEndpointIdleAgent(m, reg, windows[index%len(windows)], roots[index%len(roots)], index, running); err != nil {
			return fmt.Errorf("seed agent %d: %w", index, err)
		}
	}
	if len(reg.Projects) != claudeEndpointIdleProjects || len(reg.Windows) != claudeEndpointIdleWindows ||
		len(reg.Panes) != claudeEndpointIdlePanes || len(reg.Agents) != claudeEndpointIdleAgents {
		return fmt.Errorf("seeded shape project %d window %d pane %d agent %d", len(reg.Projects), len(reg.Windows), len(reg.Panes), len(reg.Agents))
	}
	return nil
}

func seedClaudeEndpointIdleAgent(m coremetadata.Mutator, reg *coremetadata.Registry, windowUID, root string, index int, running bool) error {
	provider := aiModeClaude
	if index%2 == 1 {
		provider = aiModeCodex
	}
	operation := fmt.Sprintf("op-idle-agent-%02d", index)
	agent, err := m.CreateAgent(reg, windowUID, coremetadata.CreateAgentOptions{Provider: provider, OperationID: operation,
		Workspace: coremetadata.AgentWorkspace{CWD: root, AdditionalWritableRoots: []string{
			fmt.Sprintf("%s/.wt/idle/agent-%02d-worktree-for-an-unrelated-roadmap-phase", root, index),
			fmt.Sprintf("/home/bench/.cache/projmux/scratch/idle-agent-%02d-unrelated-roadmap-phase", index),
		}}})
	if err != nil {
		return err
	}
	pane, err := m.AttachAgentPane(reg, agent.Metadata.UID, coremetadata.BootstrapPane{Command: provider, CWD: root}, operation)
	if err != nil {
		return err
	}
	generation, runtimeID := fmt.Sprintf("gen-idle-%02d", index), fmt.Sprintf("%%%d", 100+index)
	if _, err := m.RecordPaneActivation(reg, pane.Metadata.UID, coremetadata.PaneActivationOptions{
		Generation: generation, RuntimeID: runtimeID, AgentUID: agent.Metadata.UID, OperationID: operation,
	}); err != nil {
		return err
	}
	if _, err := m.ObservePaneActivationRuntime(reg, pane.Metadata.UID, generation, runtimeID); err != nil {
		return err
	}
	observation := coremetadata.AgentSessionObservation{Provider: provider, SessionID: fmt.Sprintf("00000000-0000-4000-8000-%012d", index)}
	if provider == aiModeClaude {
		observation.TranscriptPath = fmt.Sprintf("/home/bench/.claude/projects/-home-bench-source-repos-idle/%s.jsonl", observation.SessionID)
	} else {
		observation.ThreadID = fmt.Sprintf("019a0000-0000-7000-8000-%012d", index)
	}
	if _, _, err := m.RecordAgentSessionRef(reg, agent.Metadata.UID, observation); err != nil {
		return err
	}
	if _, err := m.SetAgentTopic(reg, agent.Metadata.UID, fmt.Sprintf("idle benchmark agent %02d on an unrelated roadmap phase: converge one owner, verify the installed smoke, and report the evidence with the merge commit, the gate results, and the residue scan, then hand the next phase back to the roadmap owner", index)); err != nil {
		return err
	}
	if running {
		return nil
	}
	exit, classification, code := coremetadata.AgentExitNormal, coremetadata.TerminationNormal, 0
	if index%3 == 0 {
		exit, classification, code = coremetadata.AgentExitAbnormal, coremetadata.TerminationAbnormal, 1
	}
	if _, err := m.RecordTermination(reg, coremetadata.TerminationEvidence{Source: coremetadata.TerminationSourceSupervisor,
		Classification: classification, PaneUID: pane.Metadata.UID, AgentUID: agent.Metadata.UID, Generation: generation,
		ExitCode: &code, OperationID: operation}); err != nil {
		return err
	}
	_, err = m.ReleaseAgentPane(reg, agent.Metadata.UID, exit, "")
	return err
}

func reportClaudeEndpointIdleRegistry(b *testing.B, f *claudeEndpointTestFixture) int64 {
	b.Helper()
	info, err := os.Stat(f.bootstrap.RegistryPath)
	if err != nil {
		b.Fatal(err)
	}
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		b.Fatal(err)
	}
	phases := map[coremetadata.AgentPhase]int{}
	for _, agent := range reg.Agents {
		phases[agent.Status.Phase]++
	}
	b.Logf("registry.json %d bytes: project %d, window %d, pane %d, agent %d %v",
		info.Size(), len(reg.Projects), len(reg.Windows), len(reg.Panes), len(reg.Agents), phases)
	return info.Size()
}

type claudeEndpointIdleRuntime struct {
	allocBytes, gcCycles, gcCPUSeconds float64
}

// runtime/metrics CPU classes only advance at GC mark termination, which is
// why the benchmark brackets its window with runtime.GC.
func readClaudeEndpointIdleRuntime() claudeEndpointIdleRuntime {
	samples := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}, {Name: "/gc/cycles/total:gc-cycles"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}}
	metrics.Read(samples)
	return claudeEndpointIdleRuntime{
		allocBytes: float64(samples[0].Value.Uint64()), gcCycles: float64(samples[1].Value.Uint64()), gcCPUSeconds: samples[2].Value.Float64(),
	}
}

func claudeEndpointProcessCPU(tb testing.TB) time.Duration {
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		tb.Fatal(err)
	}
	return time.Duration(usage.Utime.Nano() + usage.Stime.Nano())
}
