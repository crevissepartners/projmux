package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

func TestCodexHostMoveMissingAuthorityAndReplyOnlyAreMutationFree(t *testing.T) {
	for _, reply := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-authority", true: "reply-only"}[reply], func(t *testing.T) {
			f := newRelaunchFixture(t)
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Spec.Provider = aiModeCodex
			agent.Status.SessionRef = nativeTestSessionRef(nativeTestRoute("test-codex", coremetadata.CodexGenerationCurrent), "thread")
			if reply {
				agent.Metadata.Annotations[coremetadata.AnnotationAgentDialogueReplyOnly] = coremetadata.DialogueReplyOnlyOn
			}
			before := f.store.registry.Clone()
			_, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--host", "process", "--dry-run", "--socket-path", "/isolated/tmux.sock")
			if err == nil || f.store.writes != 0 || f.store.transactions != 0 || !reflect.DeepEqual(before, f.store.registry) || len(f.deletes.killed) != 0 {
				t.Fatalf("err=%v writes=%d", err, f.store.writes)
			}
		})
	}
}
func TestCodexTransferJournalPreservesFullSourceAndWaitArchive(t *testing.T) {
	f := newRelaunchFixture(t)
	source := f.agent(t).Clone()
	pane, _ := f.store.registry.Pane(personaAttachPane)
	path := filepath.Join(t.TempDir(), "transfers", "source.json")
	record := &codexHostTransferRecord{Version: 1, Source: source, Pane: pane.Clone(), Receipt: codexbroker.TransferReceipt{Token: strings.Repeat("a", 64), Source: codexbroker.TransferSource{Agent: source.Metadata.UID, Pane: pane.Metadata.UID}}, TerminatedTarget: pane, Phase: "restored"}
	if err := writeCodexHostTransfer(path, record); err != nil {
		t.Fatal(err)
	}
	got, err := readCodexHostTransfer(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(record)
	after, _ := json.Marshal(got)
	if string(before) != string(after) {
		t.Fatal("journal changed source or Wait evidence")
	}
	oversized := *record
	oversized.Source = record.Source.Clone()
	oversized.Source.Metadata.Annotations = map[string]string{"oversized": strings.Repeat("x", 1<<20)}
	if err = writeCodexHostTransfer(path, &oversized); err == nil {
		t.Fatal("unreadable oversized journal was published")
	}
	preserved, err := os.ReadFile(path)
	if err != nil || string(preserved) != string(before) {
		t.Fatal("overflow replaced previous recovery evidence")
	}
	if err = completeCodexHostTransfer(path); err != nil {
		t.Fatal(err)
	}
	if got, err = readCodexHostTransfer(path); err != nil || got != nil {
		t.Fatalf("pending journal remains: %v", err)
	}
	archived, err := readCodexHostTransfer(path + "." + record.Receipt.Token + ".completed.json")
	if err != nil || archived == nil || archived.TerminatedTarget == nil || archived.Phase != "completed" {
		t.Fatalf("lost archive: %v", err)
	}
	info, err := os.Stat(path + "." + record.Receipt.Token + ".completed.json")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("archive mode: %v", err)
	}
}

func TestCodexCompletedNativeReceiptRequiresCurrentExactWriter(t *testing.T) {
	f := newRelaunchFixture(t)
	current, _ := f.store.registry.Agent(personaAttachAgent)
	current.Spec.Provider = aiModeCodex
	current.Status.SessionRef = nativeTestSessionRef(nativeTestRoute("test-codex", coremetadata.CodexGenerationCurrent), "thread")
	current.Status.Phase = coremetadata.PhaseRunning
	pane, _ := f.store.registry.Pane(personaAttachPane)
	pane.Status.Activation = coremetadata.PaneActivation{Generation: "new-gen", OperationID: "new-op", RuntimeID: "%exact", Codex: &coremetadata.CodexActivationBinding{ThreadID: "thread", Authority: &coremetadata.CodexAuthorityRef{StateDomainID: "domain", EndpointGenerationID: "endpoint", BrokerRuntimeID: "broker", ConnectionEpoch: 2, BindingEpoch: 3}}}
	source := current.Clone()
	expected := current.Clone()
	record := &codexHostTransferRecord{Source: source, NativeExpected: &expected, Receipt: codexbroker.TransferReceipt{Source: codexbroker.TransferSource{RuntimeID: "broker"}}, NativeTarget: &codexbroker.NativeTransferTarget{Agent: current.Metadata.UID, Pane: pane.Metadata.UID, Generation: "new-gen", Operation: "new-op", RuntimeID: "%exact", Thread: "thread"}}
	before := f.store.registry.Clone()
	if err := verifyCompletedNativeTransfer(f.store.registry, record); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"generation", "runtime", "recipe", "thread"} {
		t.Run(field, func(t *testing.T) {
			changed := before.Clone()
			a, _ := changed.Agent(current.Metadata.UID)
			p, _ := changed.Pane(pane.Metadata.UID)
			switch field {
			case "generation":
				p.Status.Activation.Generation = "foreign"
			case "runtime":
				p.Status.Activation.RuntimeID = "%foreign"
			case "recipe":
				a.Spec.Workspace.CWD = "/foreign"
			case "thread":
				p.Status.Activation.Codex.ThreadID = "other"
			}
			if err := verifyCompletedNativeTransfer(changed, record); err == nil {
				t.Fatal("completed ACK authorized changed target")
			}
		})
	}
	if !reflect.DeepEqual(before, f.store.registry) {
		t.Fatal("receipt inspection mutated current writer")
	}
}

func TestCodexCompletedJournalPartialPublicationAndConflict(t *testing.T) {
	f := newRelaunchFixture(t)
	source := f.agent(t).Clone()
	pane, _ := f.store.registry.Pane(personaAttachPane)
	record := &codexHostTransferRecord{Version: 1, Source: source, Pane: pane.Clone(), Receipt: codexbroker.TransferReceipt{Token: "original", Source: codexbroker.TransferSource{Agent: source.Metadata.UID, Pane: pane.Metadata.UID}}, Phase: "ready"}
	path := filepath.Join(t.TempDir(), "pending.json")
	archive := path + ".original.completed.json"
	for _, conflict := range []bool{false, true} {
		if err := writeCodexHostTransfer(path, record); err != nil {
			t.Fatal(err)
		}
		completed := *record
		completed.Phase = "completed"
		if conflict {
			completed.Source.Metadata.Name = "changed"
		}
		if err := writeCodexHostTransfer(archive, &completed); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(archive)
		if err != nil {
			t.Fatal(err)
		}
		err = completeCodexHostTransfer(path)
		if conflict {
			if err == nil {
				t.Fatal("conflicting archive overwritten")
			}
			after, _ := os.ReadFile(archive)
			if !bytes.Equal(before, after) {
				t.Fatal("conflicting evidence changed")
			}
			if _, err = os.Stat(path); err != nil {
				t.Fatal("pending evidence removed")
			}
		} else {
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("partial publication not finalized")
			}
		}
	}
}

func TestCodexArchivePublicationMonotonicTermination(t *testing.T) {
	f := newRelaunchFixture(t)
	source := f.agent(t).Clone()
	pane, _ := f.store.registry.Pane(personaAttachPane)
	record := &codexHostTransferRecord{Version: 1, Source: source, Pane: pane.Clone(), Receipt: codexbroker.TransferReceipt{Token: "monotonic", Source: codexbroker.TransferSource{Agent: source.Metadata.UID, Pane: pane.Metadata.UID}}, Phase: "completed"}
	path := filepath.Join(t.TempDir(), "pending.json")
	if err := publishCodexHostTransferArchive(path, record); err != nil {
		t.Fatal(err)
	}
	terminated := pane.Clone()
	record.TerminatedTarget = &terminated
	record.Phase = "handoff-retired"
	if err := publishCodexHostTransferArchive(path, record); err != nil {
		t.Fatal("monotonic termination rejected", err)
	}
	archive := path + ".monotonic.completed.json"
	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if err = publishCodexHostTransferArchive(path, record); err != nil {
		t.Fatal("same evidence not idempotent", err)
	}
	changed := terminated.Clone()
	changed.Metadata.Name = "changed-target"
	record.TerminatedTarget = &changed
	if err = publishCodexHostTransferArchive(path, record); err == nil {
		t.Fatal("different observed target overwritten")
	}
	after, err := os.ReadFile(archive)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("previous termination evidence changed", err)
	}
	record.TerminatedTarget = nil
	if err = publishCodexHostTransferArchive(path, record); err == nil {
		t.Fatal("termination evidence removed")
	}
}
