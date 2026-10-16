package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

func detachedResumeCLI(t *testing.T, ctx context.Context, f processCreateCLI, args ...string) []byte {
	t.Helper()
	out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v %s", args, err, out)
	}
	return out
}

func cleanupDetachedResume(t *testing.T, f processCreateCLI, ref string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		_, _ = exec.CommandContext(ctx, f.binary, "agent", "stop", ref).CombinedOutput()
	})
}

// Ownership output precedes asynchronous snapshot persistence. Resume tests
// retire a confirmed conversation, rather than race EOF against initial init.
func awaitDetachedInitialConversation(t *testing.T, ctx context.Context, f processCreateCLI, ref string) {
	t.Helper()
	awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
		return r.ConnectionID != "" && (r.SessionID != "" || r.ThreadID != "") && r.TurnID == ""
	})
}

func TestDetachedProcessResumeSameConversationCLI(t *testing.T) {
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "first dialogue"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
			cleanupDetachedResume(t, f, first.ref)
			for _, mode := range []string{"default", "uid", "receipt", "none"} {
				args := []string{"agent", "resume", first.ref, "--detach"}
				if mode != "default" {
					args = append(args, "-o", mode)
				}
				if provider == "claude" {
					args = append(args, "--", "continue dialogue")
				}
				out := detachedResumeCLI(t, ctx, f, args...)
				if mode == "none" && len(out) != 0 {
					t.Fatalf("none output: %s", out)
				}
				if mode != "none" && !bytes.Contains(out, []byte("foreground=detached")) {
					t.Fatalf("ownership: %s", out)
				}
				next := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
					return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
				})
				if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
					t.Fatal("conversation replaced")
				}
				reg, err := f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				pane, _ := reg.Pane(next.Binding.PaneUID)
				view, ok := observeProcessHostOwner(reg, f.store.Path(), pane.Clone())
				if !ok || view.OwnerMode != processHostOwnerDetached {
					t.Fatalf("owner: %+v", view)
				}
				duplicate, err := exec.CommandContext(ctx, f.binary, "agent", "resume", first.ref, "--detach", "--", "competing").CombinedOutput()
				if err == nil || !bytes.Contains(duplicate, []byte("process-resume-owned")) {
					t.Fatalf("competing: %v %s", err, duplicate)
				}
				detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
				retired := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
				reg, err = f.store.LoadReadOnly()
				if err != nil {
					t.Fatal(err)
				}
				a, _ := reg.Agent(next.Binding.AgentUID)
				p, _ := reg.Pane(next.Binding.PaneUID)
				if a.Status.Phase != coremetadata.PhaseOffline || !coremetadata.MatchesProcessWait(next.Binding, p.Status.LastTermination) || !coremetadata.SameProcessWait(p.Status.LastTermination, a.Status.LastTermination) {
					t.Fatal("stop lacks actual Wait")
				}
				old = retired
			}
			// Detached generations still return to legacy EOF-owned resume.
			legacy := startResumeCLIInvocation(t, ctx, f, []string{"agent", "resume", first.ref, "--", "legacy foreground"})
			legacy.shutdown(t)
		})
	}
}

func TestDetachedProcessResumeClaimStopWithoutChildCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old, _ := f.store.LoadReadOnly()
			a, _ := old.Agent(strings.TrimPrefix(first.ref, "uid:"))
			p, _ := old.Pane(a.Status.PaneRef)
			c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
			path := c.deferredClaimPath(a.Metadata.UID)
			cleanupDetachedResume(t, f, first.ref)
			for _, mode := range []string{"default", "uid", "json", "receipt", "none"} {
				args := []string{"agent", "resume", first.ref, "--wait-for-peer", "--detach"}
				if mode != "default" {
					args = append(args, "-o", mode)
				}
				out := detachedResumeCLI(t, ctx, f, args...)
				if (mode != "none" && !bytes.Contains(out, []byte("claim=waiting"))) || (mode == "none" && len(out) != 0) {
					t.Fatalf("no claim output: %s", out)
				}
				if mode == "default" && !bytes.Contains(out, []byte("agent/"+a.Metadata.Name+" reused")) {
					t.Fatalf("claim kind projection: %s", out)
				}
				claim, err := readDeferredClaim(path)
				if err != nil || !claim.Detached || !deferredClaimLive(claim) {
					t.Fatalf("claim: %+v %v", claim, err)
				}
				after, _ := f.store.LoadReadOnly()
				current, _ := after.Pane(p.Metadata.UID)
				if !reflect.DeepEqual(current.Status, p.Status) {
					t.Fatal("claim invented child/generation/Wait")
				}
				for _, args := range [][]string{{"agent", "resume", first.ref, "--wait-for-peer", "--detach"}, {"agent", "resume", first.ref, "--detach", "--", "other"}} {
					out, err = exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
					if err == nil || !bytes.Contains(out, []byte("process-resume-owned")) {
						t.Fatalf("claim competition: %v %s", err, out)
					}
				}
				detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
				if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("claim remains: %v", err)
				}
				if !processIdentityAbsent(claim.Process, localipc.Process) {
					t.Fatal("claimant still alive")
				}
				after, _ = f.store.LoadReadOnly()
				current, _ = after.Pane(p.Metadata.UID)
				target, _ := after.Agent(a.Metadata.UID)
				if !reflect.DeepEqual(current.Status, p.Status) || !reflect.DeepEqual(target.Status, a.Status) {
					t.Fatal("claim stop invented Wait or changed conversation")
				}
			}
		})
	}
}

// Temporary Task7 diagnostic flag: ordinary invocations retain the original pair.
var t7ClaimPeerOrderProbe = flag.Bool("t7-claim-peer-order-probe", false, "run the bounded Task7 Darwin diagnostic batch")

type t7PeerProbeSummary struct {
	Requested       int    `json:"requested"`
	Entered         int    `json:"entered"`
	Started         int    `json:"started"`
	BodyCompleted   int    `json:"body_completed"`
	CleanupReturned int    `json:"cleanup_returned"`
	Passed          int    `json:"passed"`
	Failed          int    `json:"failed"`
	Skipped         int    `json:"skipped"`
	Stop            string `json:"stop"`
}

func t7RunPeerProbe(t *testing.T, body func(*testing.T, string, bool, *bool)) {
	s := t7PeerProbeSummary{Requested: 32, Stop: "admission-incomplete"}
	defer func() {
		raw, err := json.Marshal(s)
		const prefix = "Task7 probe summary: "
		if err != nil || len(prefix)+len(raw) > 512 {
			t.Log("Task7 probe summary: unavailable")
			return
		}
		t.Log(prefix + string(raw))
	}()
	if runtime.GOOS != "darwin" {
		s.Stop = "wrong-platform"
		return
	}
	for i := 0; i < s.Requested; i++ {
		deadline, present := t.Deadline()
		if !present || time.Until(deadline) < 55*time.Second {
			return
		}
		var completed, skipped, failed bool
		ok := t.Run(fmt.Sprintf("probe-%02d/claude", i+1), func(t *testing.T) {
			s.Entered++
			// Registered before the body, hence runs after all its cleanups.
			t.Cleanup(func() { skipped, failed = t.Skipped(), t.Failed() })
			s.Started++
			body(t, "claude", true, &completed)
		})
		s.CleanupReturned++
		if completed {
			s.BodyCompleted++
		}
		if skipped {
			s.Skipped++
		}
		if failed || !ok {
			s.Failed++
		}
		if completed && !skipped && !failed && ok {
			s.Passed++
		}
		switch {
		case failed || !ok:
			s.Stop = "first-failure"
			return
		case skipped:
			s.Stop = "skipped"
			return
		case !completed:
			s.Stop = "body-incomplete"
			return
		}
	}
	if s.Passed == s.Requested && s.BodyCompleted == s.Requested && s.CleanupReturned == s.Requested && s.Failed == 0 && s.Skipped == 0 {
		s.Stop = "complete"
	}
}

type t7JournalRow struct {
	Side        string `json:"side"`
	Peer        string `json:"peer"`
	Agent       string `json:"agent_role"`
	Group       int    `json:"helper_group"`
	WallDeltaMS int64  `json:"wall_delta_ms"`
}

type t7JournalSample struct {
	Status             string         `json:"status"`
	Phase              string         `json:"phase"`
	FailedAtCollection bool           `json:"failed_at_collection"`
	CollectedMS        int64          `json:"collected_ms"`
	Flags              uint16         `json:"flags"`
	ReadBytes          int            `json:"read_bytes"`
	Dropped            int            `json:"dropped"`
	Rows               []t7JournalRow `json:"rows"`
}

// Flags: suffix-limited, leading-row-discarded, trailing-partial, short-read,
// row-window-limited, overlong-row, file-changed, helper-group-overflow.
// Always-set bit 256: opened at collection; rotation/path replacement unverified.
func t7PeerJournal(t *testing.T, root, sourceRef, targetRef string, anchor time.Time, coreBytes int) {
	if !t.Failed() || coreBytes <= 0 {
		return
	}
	const prefix = "detached peer journal: "
	budget := min(1024, 4096-coreBytes)
	worst := t7JournalSample{Status: "unsafe-or-unavailable", Phase: "after-cleanup-attempt", FailedAtCollection: true,
		CollectedMS: 9223372036854775807, Flags: 65535, ReadBytes: 65536, Dropped: 128, Rows: []t7JournalRow{}}
	metadata, err := json.Marshal(worst)
	if err != nil || len(prefix)+len(metadata) > budget {
		return
	} // No open before capacity proof.
	s := t7ReadPeerJournal(root, sourceRef, targetRef, anchor)
	s.Phase, s.FailedAtCollection = "after-cleanup-attempt", true
	s.CollectedMS = time.Since(anchor).Milliseconds()
	for {
		raw, err := json.Marshal(s)
		if err == nil && len(prefix)+len(raw) <= budget {
			t.Log(prefix + string(raw))
			return
		}
		if len(s.Rows) == 0 {
			return
		}
		s.Rows = s.Rows[1:] // Retain the newest rows if capacity is tighter.
	}
}

// The suffix has no MessageRef: a positive row is not a receipt cause.
// Opened inode identity and byte caps do not establish wall bounds or quiescence.
func t7ReadPeerJournal(root, sourceRef, targetRef string, anchor time.Time) t7JournalSample {
	s := t7JournalSample{Status: "unsafe-or-unavailable", Flags: 256, Rows: []t7JournalRow{}}
	source, target := strings.TrimPrefix(sourceRef, "uid:"), strings.TrimPrefix(targetRef, "uid:")
	if !t7JournalAgentUID(source) || !t7JournalAgentUID(target) {
		s.Status = "missing-identity"
		return s
	}
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_CLOEXEC
	fd, err := unix.Open(root, flags|unix.O_DIRECTORY, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			s.Status = "missing"
		}
		return s
	}
	defer unix.Close(fd)
	if !t7JournalOwned(fd, unix.S_IFDIR) {
		return s
	}
	for _, name := range []string{"state", "projmux", "logs"} {
		next, err := unix.Openat(fd, name, flags|unix.O_DIRECTORY, 0)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				s.Status = "missing"
			}
			return s
		}
		defer unix.Close(next)
		if !t7JournalOwned(next, unix.S_IFDIR) {
			return s
		}
		fd = next
	}
	leaf, err := unix.Openat(fd, "operations.jsonl", flags, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			s.Status = "missing"
		}
		return s
	}
	file := os.NewFile(uintptr(leaf), "t7-owned-journal")
	if file == nil {
		_ = unix.Close(leaf)
		return s
	}
	defer file.Close()
	if !t7JournalOwned(leaf, unix.S_IFREG) {
		return s
	}
	before, err := file.Stat()
	if err != nil || before.Size() < 0 {
		return s
	}
	length := min(before.Size(), int64(64*1024))
	offset := before.Size() - length
	if offset > 0 {
		s.Flags |= 1
	}
	raw := make([]byte, int(length))
	n, readErr := file.ReadAt(raw, offset) // One bounded suffix read; not a wall-clock bound.
	s.ReadBytes = n
	raw = raw[:n]
	if n != int(length) {
		s.Flags |= 8
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		s.Status = "read-error"
		return s
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		s.Flags |= 64
	}
	if offset > 0 {
		s.Flags |= 2 // Conservative: may discard a complete row at a coincident boundary.
		if cut := bytes.IndexByte(raw, '\n'); cut >= 0 {
			raw = raw[cut+1:]
		} else {
			raw = nil
		}
	}
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		s.Flags |= 4
		if cut := bytes.LastIndexByte(raw, '\n'); cut >= 0 {
			raw = raw[:cut+1]
		} else {
			raw = nil
		}
	}
	lines := bytes.Split(raw, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 128 {
		s.Flags |= 16
		lines = lines[len(lines)-128:]
	}
	groups := map[string]int{}
	// Scan backward from file end: a long old row cannot conceal newer rows.
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if len(line) > 4096 {
			s.Flags |= 32
			s.Dropped++
			continue
		}
		fields, ok := t7JournalFields(line)
		if !ok {
			s.Dropped++
			continue
		}
		role := ""
		switch fields["agent_uid"] {
		case source:
			role = "source"
		case target:
			role = "target"
		}
		if role == "" {
			s.Dropped++
			continue
		}
		group := groups[fields["run_id"]]
		if group == 0 {
			if len(groups) < 4 {
				group = len(groups) + 1
				groups[fields["run_id"]] = group
			} else {
				s.Flags |= 128
			}
		}
		at, _ := time.Parse(time.RFC3339Nano, fields["at"])
		if len(s.Rows) < 3 {
			s.Rows = append(s.Rows, t7JournalRow{Side: strings.TrimPrefix(fields["code"], "claude.handoff."),
				Peer: fields["source"], Agent: role, Group: group, WallDeltaMS: at.Sub(anchor).Milliseconds()})
		}
	}
	for i, j := 0, len(s.Rows)-1; i < j; i, j = i+1, j-1 {
		s.Rows[i], s.Rows[j] = s.Rows[j], s.Rows[i]
	}
	s.Status = "none-observed"
	if len(s.Rows) > 0 {
		s.Status = "observed"
	}
	return s
}

func t7JournalOwned(fd, kind int) bool {
	var stat unix.Stat_t
	return unix.Fstat(fd, &stat) == nil && uint32(stat.Mode)&uint32(unix.S_IFMT) == uint32(kind) && int(stat.Uid) == os.Geteuid()
}

func t7JournalAgentUID(value string) bool {
	if !strings.HasPrefix(value, "agent-") || len(value) <= len("agent-") || len(value) > 96 {
		return false
	}
	for _, c := range value[len("agent-"):] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func t7JournalSafeID(value string) bool {
	if len(value) == 0 || len(value) > 96 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func t7JournalFields(raw []byte) (map[string]string, bool) {
	allowed := map[string]bool{"at": true, "level": true, "component": true, "event": true, "result": true,
		"duration_ms": true, "run_id": true, "version": true, "mux_backend": true, "kind": true, "code": true, "source": true, "agent_uid": true}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, false
	}
	values := map[string]string{}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] || seen[key] {
			return nil, false
		}
		seen[key] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		if key == "duration_ms" {
			if !bytes.Equal(value, []byte("0")) {
				return nil, false
			}
			continue
		}
		var text string
		if json.Unmarshal(value, &text) != nil || len(text) > 128 {
			return nil, false
		}
		values[key] = text
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, false
	}
	if _, err = decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	if len(seen) != len(allowed) {
		return nil, false
	} // Missing identity or producer field is not evidence.
	for key, value := range map[string]string{"level": "error", "component": "agent", "event": "agent.message.claude-handoff-route",
		"result": "error", "kind": "runtime", "mux_backend": "tmux"} {
		if values[key] != value {
			return nil, false
		}
	}
	if values["code"] != "claude.handoff.source-route-unproven" && values["code"] != "claude.handoff.target-route-unproven" {
		return nil, false
	}
	switch values["source"] {
	case "tmux-claude", "tmux-codex", "process-claude", "process-codex", "unknown":
	default:
		return nil, false
	}
	if !t7JournalAgentUID(values["agent_uid"]) || !t7JournalSafeID(values["run_id"]) || len(values["version"]) == 0 {
		return nil, false
	}
	for _, c := range values["version"] {
		if c < 32 || c > 126 {
			return nil, false
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, values["at"]); err != nil {
		return nil, false
	}
	return values, true
}

func TestDetachedProcessResumeClaimPeerOrderCLI(t *testing.T) {
	caseBody := func(t *testing.T, provider string, probe bool, bodyCompleted *bool) {
		var coreBytes int
		var sourceRef, targetRef string
		var journalAnchor time.Time
		f := deferredResumeCLIFixture(t, provider)
		if probe {
			t.Cleanup(func() { t7PeerJournal(t, f.root, sourceRef, targetRef, journalAnchor, coreBytes) })
		}
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		// Cache only values observed by this test's original calls. Emission
		// does no state/IPC/process reads and never replays a failed poll.
		started := time.Now()
		if probe {
			journalAnchor = started
		}
		type validReceipt struct {
			At                     time.Duration
			State, Reason, Unknown string
		}
		type peerPoll struct {
			Ref, Stage                         string
			Started, PollStarted, PollFinished time.Duration
			Polls                              int
			Command, Decode                    string
			LastValid                          validReceipt
		}
		var diagnostic struct {
			Stage, Phase                  string
			BodyPassedBeforeSourceCleanup bool
			StageAt, EmittedAt            time.Duration
			EnqueueRef, EnqueueResult     string
			Record                        struct {
				Sampled                                                 bool
				At                                                      time.Duration
				BindingOwnerSame, GenerationChanged                     bool
				SessionSame, ThreadSame, ConnectionPresent, TurnPresent bool
			}
			NotSampled [4]string
			Peers      [3]peerPoll
		}
		diagnostic.NotSampled = [4]string{"source-record", "host", "lease", "pending"}
		for i, ref := range []string{"detached-first", "detached-second", "detached-third"} {
			diagnostic.Peers[i] = peerPoll{Ref: ref, Stage: "not-sampled", Command: "not-sampled", Decode: "not-sampled", LastValid: validReceipt{State: "not-sampled", Reason: "not-sampled", Unknown: "not-sampled"}}
		}
		stage := func(name string) {
			diagnostic.Stage, diagnostic.StageAt = name, time.Since(started)
		}
		emitted := false
		emit := func(phase string) {
			if !t.Failed() || emitted {
				return
			}
			emitted = true
			diagnostic.Phase = phase
			diagnostic.EmittedAt = time.Since(started)
			raw, err := json.Marshal(diagnostic)
			const prefix = "detached peer cached failure: "
			if err != nil || len(prefix)+len(raw) > 4096 {
				t.Log("detached peer cached failure: encoding unavailable or size limit")
				return
			}
			if probe {
				coreBytes = len(prefix) + len(raw)
			}
			t.Log(prefix + string(raw))
		}
		defer func() {
			phase := "setup-fallback"
			if diagnostic.BodyPassedBeforeSourceCleanup {
				phase = "source-cleanup-failure-after-body-pass"
			}
			emit(phase)
		}()
		stage("initial-owned")
		first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
		stage("initial-conversation")
		awaitDetachedInitialConversation(t, ctx, f, first.ref)
		stage("initial-wait")
		first.shutdown(t)
		stage("retired-record")
		old := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool { return r.ResumeState == coremetadata.ProcessResumable })
		stage("source-owned")
		source := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--name", "source", "--", "source"))
		if probe {
			sourceRef, targetRef = source.ref, first.ref
		}
		defer source.shutdown(t)
		defer func() {
			// This boundary precedes the original source shutdown defer.
			// Passing it is not evidence of source cleanup or provider Wait.
			diagnostic.BodyPassedBeforeSourceCleanup = !t.Failed()
			emit("body-failure-before-source-cleanup")
		}()
		cleanupDetachedResume(t, f, first.ref)
		stage("claim-handshake")
		detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--wait-for-peer", "--detach")
		c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
		stage("claim-read")
		claim, err := readDeferredClaim(c.deferredClaimPath(old.Binding.AgentUID))
		if err != nil {
			t.Fatal(err)
		}
		process, err := os.FindProcess(claim.Process.PID)
		if err != nil {
			t.Fatal(err)
		}
		stage("sigstop")
		if err = process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = process.Signal(syscall.SIGCONT) })
		for i, ref := range []string{"detached-first", "detached-second", "detached-third"} {
			stage("enqueue")
			diagnostic.EnqueueRef, diagnostic.EnqueueResult = ref, "in-flight"
			processCLIUntil(t, ctx, func() bool {
				out, err := exec.CommandContext(ctx, f.binary, "agent", "message", "send", first.ref, "--source", source.ref, "--message-ref", ref, "--", "peer-"+string(rune('0'+i))).CombinedOutput()
				diagnostic.EnqueueResult = "other-error"
				if err == nil {
					held := bytes.Contains(out, []byte("held"))
					diagnostic.EnqueueResult = "ok-not-held"
					if held {
						diagnostic.EnqueueResult = "ok-held"
					}
					return held
				}
				if bytes.Contains(out, []byte("source Agent is not eligible")) {
					diagnostic.EnqueueResult = "source-ineligible"
					return false
				}
				t.Fatalf("peer: %v %s", err, out)
				return false
			})
		}
		stage("sigcont")
		if err = process.Signal(syscall.SIGCONT); err != nil {
			t.Fatal(err)
		}
		stage("resumed-record")
		next := awaitProcessResumeRecord(t, ctx, f, first.ref, func(r *coremetadata.ProcessSessionRecord) bool {
			diagnostic.Record.Sampled, diagnostic.Record.At = true, time.Since(started)
			diagnostic.Record.BindingOwnerSame = r.Binding.AgentUID == old.Binding.AgentUID && r.Binding.PaneUID == old.Binding.PaneUID && r.Binding.ProjectUID == old.Binding.ProjectUID && r.Binding.WindowUID == old.Binding.WindowUID
			diagnostic.Record.GenerationChanged = r.Binding.Generation != old.Binding.Generation
			diagnostic.Record.SessionSame, diagnostic.Record.ThreadSame = r.SessionID == old.SessionID, r.ThreadID == old.ThreadID
			diagnostic.Record.ConnectionPresent, diagnostic.Record.TurnPresent = r.ConnectionID != "", r.TurnID != ""
			return r.Binding.Generation != old.Binding.Generation && r.ConnectionID != ""
		})
		stage("conversation-check")
		if next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
			t.Fatal("peer replaced dialogue")
		}
		// Same Output/JSON/terminal/return operations as deferredCLIStatus;
		// the extra fields are value copies, never aliases of its decode map.
		status := func(index int, ref, state string) map[string]any {
			t.Helper()
			poll := &diagnostic.Peers[index]
			poll.Started = time.Since(started)
			var receipt map[string]any
			processCLIUntil(t, ctx, func() bool {
				poll.Stage = "command"
				poll.Polls++
				poll.PollStarted, poll.Command, poll.Decode = time.Since(started), "in-flight", "not-sampled"
				raw, err := exec.CommandContext(ctx, f.binary, "agent", "message", "status", ref, "-o", "json").Output()
				poll.PollFinished = time.Since(started)
				poll.Stage = "command-returned"
				poll.Command, poll.Decode = "ok", "not-sampled"
				if err != nil {
					poll.Command = "start-or-wait-error"
					var exit *exec.ExitError
					switch {
					case errors.Is(ctx.Err(), context.DeadlineExceeded):
						poll.Command = "context-deadline"
					case errors.Is(ctx.Err(), context.Canceled):
						poll.Command = "context-cancel"
					case errors.As(err, &exit):
						poll.Command = "exit-nonzero"
					}
					return false
				}
				poll.Stage = "decode"
				decodeErr := json.Unmarshal(raw, &receipt)
				if decodeErr != nil {
					poll.Decode = "decode-error"
					var syntax *json.SyntaxError
					var shape *json.UnmarshalTypeError
					if errors.As(decodeErr, &syntax) {
						poll.Decode = "syntax-error"
					} else if errors.As(decodeErr, &shape) {
						poll.Decode = "type-error"
					}
					return false
				}
				delivery, _ := receipt["delivery"].(map[string]any)
				poll.Decode = "valid"
				if delivery == nil {
					poll.Decode = "missing-delivery"
				}
				latest := validReceipt{At: poll.PollFinished, State: "other", Reason: "other", Unknown: "not-sampled"}
				if value, ok := delivery["state"].(string); ok {
					switch value {
					case "held", "accepted", "delivered", "failed", "refused", "expired", "stale":
						latest.State = value
					}
				}
				if value, ok := delivery["reason"].(string); ok {
					switch value {
					case "", "target-awaiting-resume", "target-turn-active", "provider-resume-first-frame", "provider-native-peer-joined", "provider-prewrite-refused", "broker-handoff-persist-failed", "broker-delivery-persist-failed", "provider-handoff-outcome-unknown", "stale-binding", "target-activation-stale", "deadline-expired":
						latest.Reason = value
					}
				}
				if value, ok := delivery["outcomeUnknown"].(bool); ok {
					latest.Unknown = fmt.Sprint(value)
				}
				if delivery != nil {
					poll.LastValid = latest
				}
				poll.Stage = "terminal-check"
				if terminal := delivery["state"]; terminal != "held" && terminal != "accepted" && terminal != state {
					t.Fatalf("unexpected terminal state want %s: %s", state, raw)
				}
				return delivery["state"] == state
			})
			poll.Stage = "complete"
			return receipt
		}
		for i, ref := range []string{"detached-first", "detached-second", "detached-third"} {
			stage("receipt-" + ref)
			status(i, ref, "delivered")
		}
		stage("target-stop-wait")
		detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
		stage("wire-read")
		wire, err := os.ReadFile(f.trace)
		if err != nil {
			t.Fatal(err)
		}
		var peerFrames []string
		for line := range bytes.SplitSeq(bytes.TrimSpace(wire), []byte("\n")) {
			var frame map[string]any
			if json.Unmarshal(line, &frame) != nil {
				t.Fatal("wire JSON")
			}
			text := ""
			if frame["type"] == "user" {
				text, _ = frame["message"].(map[string]any)["content"].(string)
			}
			if frame["method"] == "turn/start" {
				inputs := frame["params"].(map[string]any)["input"].([]any)
				text, _ = inputs[0].(map[string]any)["text"].(string)
			}
			if strings.Contains(text, "projmux-coordination") {
				peerFrames = append(peerFrames, text)
			}
		}
		if provider == "claude" {
			raw, err := os.ReadFile(filepath.Join(f.root, "messages.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for line := range bytes.SplitSeq(bytes.TrimSpace(raw), []byte("\n")) {
				var frame map[string]any
				if json.Unmarshal(line, &frame) != nil {
					t.Fatal(err)
				}
				peerFrames = append(peerFrames, frame["message"].(map[string]any)["content"].(string))
			}
		}
		stage("wire-count-order")
		if len(peerFrames) != 3 {
			t.Fatalf("peer count: %q", peerFrames)
		}
		for i, text := range peerFrames {
			var frame map[string]any
			_ = json.Unmarshal([]byte(text), &frame)
			if frame["messageRef"] != []string{"detached-first", "detached-second", "detached-third"}[i] || frame["authority"] != "untrusted-coordination-only" || frame["payload"] != fmt.Sprintf("peer-%d", i) {
				t.Fatal("peer promoted or reordered", frame)
			}
		}

		if bodyCompleted != nil {
			*bodyCompleted = true
		}
	}
	if *t7ClaimPeerOrderProbe {
		t7RunPeerProbe(t, caseBody)
		return
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) { caseBody(t, provider, false, nil) })
	}
}

func TestDetachedProcessResumeFailurePreservesConversationCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old, _ := f.store.LoadReadOnly()
			for _, args := range [][]string{{"agent", "resume", first.ref, "--detach", "-o", "pane-id"}, {"agent", "resume", first.ref, "--detach", "-o", "invalid"}, {"agent", "resume", first.ref, "--detach", "--dialogue-reply-only"}, {"agent", "resume", first.ref, "--detach", "--wait-for-peer", "--", "invalid"}} {
				out, err := exec.CommandContext(ctx, f.binary, args...).CombinedOutput()
				if err == nil {
					t.Fatalf("invalid accepted: %s", out)
				}
				after, _ := f.store.LoadReadOnly()
				if !reflect.DeepEqual(after, old) {
					t.Fatal("invalid arguments mutated Registry")
				}
			}
			cleanupDetachedResume(t, f, first.ref)
			a, _ := old.Agent(strings.TrimPrefix(first.ref, "uid:"))
			originalPane, _ := old.Pane(a.Status.PaneRef)
			caller, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			wire := processDetachedResumeRequest{Agent: a.Metadata.UID, Prompt: processResumeFirstFrame{Kind: "user", Text: "rollback"}, Previous: originalPane.Status.ProcessSession.Binding}
			command := &createCommand{store: newResourceStore()}
			err = launchProcessDetached(ctx, f.binary, processDetachedRequest{Resume: &wire, Caller: caller, Output: cli.OutputModeDefault, RegistryPath: f.store.Path()}, command, detachedOutputFailureWriter{}, &bytes.Buffer{})
			if !errors.Is(err, errDetachedOutputFixture) || strings.Contains(err.Error(), "cleanup-unconfirmed") {
				t.Fatalf("output rollback: %v", err)
			}
			after, _ := f.store.LoadReadOnly()
			target, _ := after.Agent(a.Metadata.UID)
			if target == nil || target.Status.Phase != coremetadata.PhaseOffline {
				t.Fatal("Agent not preserved offline")
			}
			pane, _ := after.Pane(target.Status.PaneRef)
			if pane == nil {
				t.Fatal("Pane not preserved")
			}
			original, _ := old.Pane(a.Status.PaneRef)
			if pane.Status.ProcessSession.SessionID != original.Status.ProcessSession.SessionID || pane.Status.ProcessSession.ThreadID != original.Status.ProcessSession.ThreadID || !coremetadata.MatchesProcessWait(pane.Status.ProcessSession.Binding, pane.Status.LastTermination) {
				t.Fatal("rollback lost dialogue or Wait")
			}
			// A rollback is retryable by the same public path, with no new conversation.
			detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--detach", "--", "retry")
			detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
		})
	}
}

func TestDetachedProcessResumeTranscriptCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := processResumeCLIFixture(t, provider)
			diagnostic := newClaudeAuthorityFailureDiagnostics(t)
			if provider == "claude" {
				diagnostic.paths = []string{f.store.Path()}
			}
			defer diagnostic.beforeCleanup()
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(f.root, ".claude"))
			scriptPath := filepath.Join(f.root, "provider.py")
			conversation := "process-session"
			transcript := filepath.Join(f.root, ".claude", "projects", aisessions.EncodeClaudeProjectPath(f.root), conversation+".jsonl")
			meta := ""
			if provider == "codex" {
				prepareProcessCodexPeerCLI(t, f)
				scriptPath = filepath.Join(f.root, "codex-provider.py")
				conversation = "process-thread"
				transcript = filepath.Join(f.root, ".codex", "sessions", "rollout-"+conversation+".jsonl")
				raw, _ := json.Marshal(map[string]any{"type": "session_meta", "payload": map[string]any{"id": conversation, "cwd": f.root, "timestamp": "2026-10-05T00:00:00Z"}})
				meta = string(raw) + "\n"
			}
			script, err := os.ReadFile(scriptPath)
			if err != nil {
				t.Fatal(err)
			}
			pathJSON, _ := json.Marshal(transcript)
			metaJSON, _ := json.Marshal(meta)
			text := "import os,json\nos.makedirs(os.path.dirname(" + string(pathJSON) + "),exist_ok=True)\nopen(" + string(pathJSON) + ",'a').write(" + string(metaJSON) + ")\n" + string(script)
			response := `{"type":"assistant","message":{"content":"detached fixture reply"}}` + "\n"
			if provider == "claude" {
				responseJSON, _ := json.Marshal(response)
				text = strings.Replace(text, "with write_lock: print(json.dumps(frame),flush=True)", "with write_lock:\n  if frame.get('type')=='assistant':open("+string(pathJSON)+",'a').write("+string(responseJSON)+")\n  print(json.dumps(frame),flush=True)", 1)
				text = strings.Replace(text, "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close()", "emit({'type':'assistant','session_id':'process-session','message_echo':m});c.close();emit({'type':'result','subtype':'success','session_id':'process-session'});continue", 1)
			} else {
				response = `{"type":"event_msg","payload":{"type":"agent_message","message":"detached fixture reply"}}` + "\n"
				responseJSON, _ := json.Marshal(response)
				text = strings.Replace(text, "if prompt=='controls' or prompt.startswith('{'):", "if prompt=='controls':", 1)
				text = strings.Replace(text, "def complete():open", "def complete():open("+string(pathJSON)+",'a').write("+string(responseJSON)+");notify('item/agentMessage/delta',{'threadId':'process-thread','turnId':current,'itemId':'reply-'+current,'delta':'detached fixture reply'});open", 1)
			}
			if err = os.WriteFile(scriptPath, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, f.binary, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--detach", "--", "initial dialogue")...).CombinedOutput()
			if err != nil {
				t.Fatalf("detached dialogue create: %v %s", err, out)
			}
			fields := strings.Fields(string(out))
			if len(fields) < 2 {
				t.Fatalf("no ownership: %s", out)
			}
			ref := fields[1]
			t.Cleanup(func() {
				cleanup, stop := context.WithTimeout(context.Background(), 12*time.Second)
				defer stop()
				_, _ = exec.CommandContext(cleanup, f.binary, "agent", "stop", ref).CombinedOutput()
			})
			old := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.ConnectionID != "" && (r.SessionID != "" || r.ThreadID != "")
			})
			reg, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := reg.Agent(strings.TrimPrefix(ref, "uid:"))
			pane, _ := reg.Pane(agent.Status.PaneRef)
			view, ok := observeProcessHostOwner(reg, f.store.Path(), pane.Clone())
			if !ok || view.OwnerMode != processHostOwnerDetached || view.Binding != processSchemaBinding(old.Binding) {
				t.Fatalf("detached owner binding: %+v", view)
			}
			if provider == "claude" {
				processCLIUntil(t, ctx, func() bool {
					latest, err := f.store.LoadReadOnly()
					if err != nil {
						return false
					}
					_, ready := discoverProcessClaudeProof(f.store.Path(), latest, strings.TrimPrefix(ref, "uid:"))
					return ready
				})
			}
			out, err = exec.CommandContext(ctx, f.binary, "agent", "message", "send", ref, "--source", ref, "--", "continue independent dialogue").CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte("delivered")) {
				t.Fatalf("peer dialogue: %v %s", err, out)
			}
			processCLIUntil(t, ctx, func() bool {
				raw, err := os.ReadFile(transcript)
				return err == nil && bytes.Contains(raw, []byte("detached fixture reply"))
			})
			out, err = exec.CommandContext(ctx, f.binary, "agent", "sessions", "list", ref, "-o", "json").CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte(conversation)) {
				t.Fatalf("detached sessions: %v %s", err, out)
			}
			out, err = exec.CommandContext(ctx, f.binary, "agent", "stop", ref).CombinedOutput()
			if err != nil {
				t.Fatalf("dialogue stop: %v %s", err, out)
			}
			detachedResumeCLI(t, ctx, f, "agent", "resume", ref, "--detach", "--", "continue same dialogue")
			next := awaitProcessResumeRecord(t, ctx, f, ref, func(r *coremetadata.ProcessSessionRecord) bool {
				return r.Binding.Generation != old.Binding.Generation && (r.SessionID != "" || r.ThreadID != "")
			})
			if next.Binding.AgentUID != old.Binding.AgentUID || next.Binding.PaneUID != old.Binding.PaneUID || next.SessionID != old.SessionID || next.ThreadID != old.ThreadID {
				t.Fatal("detached resume replaced dialogue")
			}
			before, _ := os.ReadFile(transcript)
			processCLIUntil(t, ctx, func() bool {
				out, err = exec.CommandContext(ctx, f.binary, "agent", "message", "send", ref, "--source", ref, "--", "reply after detached resume").CombinedOutput()
				if err == nil {
					return bytes.Contains(out, []byte("delivered"))
				}
				if bytes.Contains(out, []byte("not eligible")) || bytes.Contains(out, []byte("registration lease")) {
					return false
				}
				t.Fatalf("resumed peer: %v %s", err, out)
				return false
			})
			processCLIUntil(t, ctx, func() bool {
				raw, err := os.ReadFile(transcript)
				return err == nil && len(raw) > len(before) && bytes.Contains(raw, []byte("detached fixture reply"))
			})
			out, err = exec.CommandContext(ctx, f.binary, "agent", "sessions", "list", ref, "-o", "json").CombinedOutput()
			if err != nil || !bytes.Contains(out, []byte(conversation)) {
				t.Fatalf("resumed sessions: %v %s", err, out)
			}
			detachedResumeCLI(t, ctx, f, "agent", "stop", ref)
			out, err = exec.CommandContext(ctx, f.binary, "delete", "agent", ref, "--dry-run").CombinedOutput()
			if err != nil {
				t.Fatalf("dialogue delete preview: %v %s", err, out)
			}
			out, err = exec.CommandContext(ctx, f.binary, "delete", "agent", ref, "--yes").CombinedOutput()
			if err != nil {
				t.Fatalf("dialogue delete: %v %s", err, out)
			}
		})
	}
}

func TestDetachedProcessResumeClaimOutputRollbackCLI(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			old, err := f.store.LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			agent, _ := old.Agent(strings.TrimPrefix(first.ref, "uid:"))
			pane, _ := old.Pane(agent.Status.PaneRef)
			caller, _, err := localipc.Process(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			wire := processDetachedResumeRequest{Agent: agent.Metadata.UID, WaitForPeer: true, Previous: pane.Status.ProcessSession.Binding}
			command := &createCommand{store: newResourceStore()}
			err = launchProcessDetached(ctx, f.binary, processDetachedRequest{Resume: &wire, Caller: caller, Output: cli.OutputModeDefault, RegistryPath: f.store.Path()}, command, detachedOutputFailureWriter{}, &bytes.Buffer{})
			if !errors.Is(err, errDetachedOutputFixture) || strings.Contains(err.Error(), "cleanup-unconfirmed") {
				t.Fatalf("claim rollback: %v", err)
			}
			c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
			if _, err = os.Stat(c.deferredClaimPath(agent.Metadata.UID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("claim leaked: %v", err)
			}
			current, err := f.store.LoadReadOnly()
			if err != nil || !reflect.DeepEqual(current, old) {
				t.Fatal("child-free rollback changed Registry", err)
			}
		})
	}
}

func TestDetachedProcessResumeStaleClaimStopCLI(t *testing.T) {
	f := deferredResumeCLIFixture(t, "codex")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", "codex", "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
	awaitDetachedInitialConversation(t, ctx, f, first.ref)
	first.shutdown(t)
	cleanupDetachedResume(t, f, first.ref)
	detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--detach", "--wait-for-peer")
	c := &agentCommand{messagePaths: agentMessagePaths{registryPath: f.store.Path()}}
	path := c.deferredClaimPath(strings.TrimPrefix(first.ref, "uid:"))
	claim, err := readDeferredClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(claim.Process.PID)
	if err != nil {
		t.Fatal(err)
	}
	if err = process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	released := false
	t.Cleanup(func() {
		if !released && deferredClaimLive(claim) {
			_ = writeDeferredClaim(path, claim)
			_ = process.Signal(syscall.SIGCONT)
		}
	})
	changed := claim
	changed.Binding.Generation = "gen-changed"
	if err = writeDeferredClaim(path, changed); err != nil {
		t.Fatal(err)
	}
	before, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(ctx, f.binary, "agent", "stop", first.ref).CombinedOutput()
	if err == nil {
		t.Fatalf("stale claim stop succeeded: %s", out)
	}
	after, _ := f.store.LoadReadOnly()
	if !reflect.DeepEqual(before, after) || !deferredClaimLive(claim) {
		t.Fatal("stale stop signaled owner or changed Wait")
	}
	if err = writeDeferredClaim(path, claim); err != nil {
		t.Fatal(err)
	}
	if err = process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
	released = true
}
