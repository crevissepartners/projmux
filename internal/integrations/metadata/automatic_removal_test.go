package metadata

import (
	"encoding/json"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"os"
	"path/filepath"
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
			path := filepath.Join(filepath.Dir(filepath.Dir(store.Path())), DeletionRecordsFile)
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
			if mode == "protected" || mode == "invalid-decodable" {
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
			unknown := mode == "malformed" || mode == "missing"
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
				if unknown {
					if rows[0].Actor.Basis != "protection-unavailable" || len(rows[0].Affected) != 0 {
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
