package metadata

import (
	"encoding/json"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAutomaticRecoveryProtectionAndRecords(t *testing.T) {
	for _, mode := range []string{"unprotected", "protected", "same-uid", "invalid-decodable", "malformed", "missing"} {
		t.Run(mode, func(t *testing.T) {
			store, _ := seedRecoveryStore(t, 0)
			source := filepath.Join(t.TempDir(), "source.json")
			reg, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			sourceBytes, err := json.Marshal(reg)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(source, sourceBytes, 0600); err != nil {
				t.Fatal(err)
			}
			mutator := testMutator(map[string]bool{"/src/projmux": true})
			var agentUID string
			_, err = store.Update(func(current *coremetadata.Registry) error {
				opts := coremetadata.CreateAgentOptions{Provider: "codex", Workspace: coremetadata.AgentWorkspace{CWD: "/src/projmux"}}
				if mode != "unprotected" {
					opts.Labels = map[string]string{coremetadata.LabelProtected: "true"}
				}
				agent, e := mutator.CreateAgent(current, current.Windows[0].Metadata.UID, opts)
				agentUID = agent.Metadata.UID
				return e
			})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "same-uid" {
				current, e := store.Load()
				if e != nil {
					t.Fatal(e)
				}
				a, _ := current.Agent(agentUID)
				a.Metadata.Labels = nil
				data, e := json.Marshal(current)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(source, data, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "invalid-decodable" {
				current, e := store.Load()
				if e != nil {
					t.Fatal(e)
				}
				current.NameReservations = nil
				data, e := json.Marshal(current)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(store.Path(), data, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "malformed" {
				if err = os.WriteFile(store.Path(), []byte("{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing" {
				if err = os.Remove(store.Path()); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(store.Path())
			result, err := store.RestoreFrom(RestoreRequest{SourcePath: source})
			path := filepath.Join(store.deletionStateDir, DeletionRecordsFile)
			data, _ := os.ReadFile(path)
			var rows []DeletionRecord
			for line := range strings.SplitSeq(string(data), "\n") {
				if line != "" {
					var row DeletionRecord
					if e := json.Unmarshal([]byte(line), &row); e != nil {
						t.Fatal(e)
					}
					rows = append(rows, row)
				}
			}
			if mode == "protected" {
				if err == nil || !strings.Contains(err.Error(), "agent-protected") {
					t.Fatalf("refusal=%v", err)
				}
				after, _ := os.ReadFile(store.Path())
				if string(after) != string(before) || len(rows) != 0 {
					t.Fatal("refusal wrote Registry or deletion record")
				}
				return
			}
			if err != nil || !result.Changed {
				t.Fatalf("restore=%+v %v", result, err)
			}
			unknown := mode == "malformed" || mode == "missing" || mode == "invalid-decodable"
			if result.ProtectionUnavailable != unknown {
				t.Fatalf("protectionUnavailable=%t", result.ProtectionUnavailable)
			}
			if mode == "same-uid" {
				if len(rows) != 0 {
					t.Fatal("retained UID recorded as deletion")
				}
				current, e := store.Load()
				if e != nil {
					t.Fatal(e)
				}
				if _, ok := current.Agent(agentUID); !ok {
					t.Fatal("same UID lost")
				}
			} else {
				if len(rows) != 1 {
					t.Fatalf("rows=%+v", rows)
				}
				if mode == "invalid-decodable" {
					if len(result.ProtectionUnavailableAgentUIDs) != 1 || result.ProtectionUnavailableAgentUIDs[0] != agentUID || len(rows[0].Affected) != 1 || rows[0].Affected[0].UID != agentUID {
						t.Fatalf("degraded removal evidence: %+v %+v", result, rows)
					}
				}
				if unknown {
					if rows[0].Actor.Basis != "protection-unavailable" || (mode != "invalid-decodable" && len(rows[0].Affected) != 0) {
						t.Fatalf("unknown row=%+v", rows[0])
					}
				} else if rows[0].Affected[0].UID != agentUID {
					t.Fatalf("deletion row=%+v", rows[0])
				}
			}
			repeat, e := store.RestoreFrom(RestoreRequest{SourcePath: source})
			if e != nil || repeat.Changed {
				t.Fatalf("repeat=%+v %v", repeat, e)
			}
			repeated, _ := os.ReadFile(path)
			if string(repeated) != string(data) {
				t.Fatal("repeat wrote duplicate records")
			}
		})
	}
}

func TestAutomaticRemovalRowsShareOperationIDAndConfiguredJournalRoot(t *testing.T) {
	root := t.TempDir()
	store := NewDefaultStore(config.Paths{StateDir: root})
	if store.deletionStateDir != root {
		t.Fatalf("default journal root=%s", store.deletionStateDir)
	}
	// A custom recovery file must not decide the journal's configured location.
	custom := NewStore(filepath.Join(t.TempDir(), "custom.json"))
	custom.deletionStateDir = root
	before := coremetadata.Registry{Agents: []coremetadata.Agent{
		{Metadata: coremetadata.ObjectMeta{UID: "agent-one", Name: "one"}},
		{Metadata: coremetadata.ObjectMeta{UID: "agent-two", Name: "two"}},
	}}
	RecordAutomaticRemovals(func() (string, error) { return custom.deletionStateDir, nil }, before, coremetadata.Registry{}, "project-fresh", io.Discard)
	data, err := os.ReadFile(filepath.Join(root, DeletionRecordsFile))
	if err != nil {
		t.Fatal(err)
	}
	var rows []DeletionRecord
	for line := range strings.SplitSeq(string(data), "\n") {
		if line != "" {
			var row DeletionRecord
			if err = json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatal(err)
			}
			rows = append(rows, row)
		}
	}
	if len(rows) != 2 || rows[0].OperationID != rows[1].OperationID || !regexp.MustCompile(`^op-[0-9a-f]{16}$`).MatchString(rows[0].OperationID) {
		t.Fatalf("operation rows=%+v", rows)
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(custom.Path()), DeletionRecordsFile)); !os.IsNotExist(err) {
		t.Fatalf("custom Registry path guessed journal location: %v", err)
	}
}
