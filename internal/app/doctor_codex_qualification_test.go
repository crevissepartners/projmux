package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

func doctorQualificationFixture(t *testing.T, name string) (*doctorCommand, *codexupgrade.QualificationStore, string) {
	t.Helper()
	root := t.TempDir()
	stateHome := filepath.Join(root, "state")
	store := codexupgrade.NewQualificationStateStore(filepath.Join(stateHome, "projmux"))
	receipt := *doctorQualifiedVersions()
	receipt.Versions = codexgeneration.VersionPair{Old: "0.153.2", New: "0.153.4"}
	switch name {
	case "stored", "corrupt", "mismatch", "refused", "unbacked":
		if name == "refused" {
			receipt = codexgeneration.EvaluateQualification(receipt.Versions, codexgeneration.QualificationEvidence{})
		}
		if name == "unbacked" {
			receipt.Evidence.ObservedThreadTurns = 0
		}
		if err := store.Save(receipt); err != nil {
			t.Fatal(err)
		}
		path, err := store.Path(receipt.Versions)
		if err != nil {
			t.Fatal(err)
		}
		if name == "corrupt" || name == "mismatch" {
			body := []byte(`{"private": "payload-must-not-appear"}`)
			if name == "mismatch" {
				body = mustReceiptJSON(t, *doctorQualifiedVersions())
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	case "unavailable":
		if err := os.MkdirAll(filepath.Dir(store.Dir()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.Dir(), []byte("private-store-error"), 0o600); err != nil {
			t.Fatal(err)
		}
	case "absent":
	default:
		t.Fatalf("unknown fixture %q", name)
	}
	lookup := func(name string) string {
		if name == "XDG_STATE_HOME" {
			return stateHome
		}
		return ""
	}
	defaults := newDoctorCommand()
	defaults.getenv = lookup
	cmd := newStubDoctorCommand("linux", map[string]bool{"tmux": true, "git": true, "stty": true})
	cmd.getenv = lookup
	cmd.codexQualification = defaults.codexQualification
	return cmd, store, root
}

func TestDoctorStoredCodexQualificationWithoutJournal(t *testing.T) {
	cmd, store, root := doctorQualificationFixture(t, "stored")
	if _, exists, err := codexupgrade.NewStateStore(filepath.Join(root, "state", "projmux")).Load(); exists || err != nil {
		t.Fatalf("fixture must have no generation journal: exists=%t err=%v", exists, err)
	}
	// A Registry read failure cannot suppress an independent qualification
	// store observation either.
	cmd.readRegistry = func() (coremetadata.Registry, error) { return coremetadata.Registry{}, errors.New("unavailable") }
	cmd.codexGeneration = func(coremetadata.Registry) *doctorCodexGenerationPool {
		t.Fatal("generation journal should not be consulted")
		return nil
	}
	for _, args := range [][]string{nil, {"--section", "integrations"}} {
		var out bytes.Buffer
		if err := cmd.Run(args, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		want := "0.153.2 -> 0.153.4: stored; verdict: yes; reason: qualified; qualification ready: true"
		if !strings.Contains(out.String(), want) {
			t.Fatalf("journal-free receipt omitted from doctor: %s", out.String())
		}
	}
	entries, err := store.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("fixture store changed: %+v %v", entries, err)
	}
}

func TestDoctorStoredCodexQualificationTextJSONSupportParity(t *testing.T) {
	for _, tc := range []struct {
		name, status, rowStatus, verdict string
		ready                            bool
	}{
		{"stored", "stored", "stored", "yes", true},
		{"absent", "absent", "", "", false},
		{"corrupt", "damaged", "damaged", "", false},
		{"mismatch", "damaged", "version-pair-mismatch", "", false},
		{"unavailable", "unavailable", "", "", false},
		{"refused", "stored", "stored", "no", false},
		{"unbacked", "stored", "stored", "yes", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, _, root := doctorQualificationFixture(t, tc.name)
			before := snapshotDoctorTree(t, root)
			var textOut, jsonOut bytes.Buffer
			if err := cmd.Run([]string{"--section", "integrations"}, &textOut, io.Discard); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Run([]string{"--section", "integrations", "--json"}, &jsonOut, io.Discard); err != nil {
				t.Fatal(err)
			}
			var direct doctorReport
			if err := json.Unmarshal(jsonOut.Bytes(), &direct); err != nil {
				t.Fatal(err)
			}
			data, err := (&diagnosticsCommand{doctor: cmd}).supportDoctorJSON()
			if err != nil {
				t.Fatal(err)
			}
			var support doctorReport
			if err := json.Unmarshal(data, &support); err != nil {
				t.Fatal(err)
			}
			report := direct.CodexQualification
			if report == nil || report.Status != tc.status || !reflect.DeepEqual(report, support.CodexQualification) {
				t.Fatalf("doctor/support parity: direct=%+v support=%+v", report, support.CodexQualification)
			}
			if !strings.Contains(textOut.String(), "Store: "+tc.status) {
				t.Fatalf("text store status = %s", textOut.String())
			}
			if tc.rowStatus == "" {
				if report.VersionPairs == nil || len(report.VersionPairs) != 0 {
					t.Fatalf("absent/unavailable receipts = %+v", report.VersionPairs)
				}
			} else {
				if len(report.VersionPairs) != 1 {
					t.Fatalf("receipts = %+v", report.VersionPairs)
				}
				row := report.VersionPairs[0]
				if row.OldVersion != "0.153.2" || row.NewVersion != "0.153.4" || row.Status != tc.rowStatus || string(row.Verdict) != tc.verdict {
					t.Fatalf("receipt = %+v", row)
				}
				want := row.OldVersion + " -> " + row.NewVersion + ": " + row.Status
				if tc.verdict != "" {
					if row.QualificationReady == nil || *row.QualificationReady != tc.ready {
						t.Fatalf("gate readiness = %+v", row)
					}
					want += fmt.Sprintf("; verdict: %s; reason: %s; qualification ready: %t", row.Verdict, row.Reason, tc.ready)
				} else if row.QualificationReady != nil || row.Reason != "" {
					t.Fatalf("damaged receipt claimed a verdict: %+v", row)
				}
				if !strings.Contains(textOut.String(), want) {
					t.Fatalf("text lost receipt fields: %s", textOut.String())
				}
			}
			for _, secret := range []string{root, "payload-must-not-appear", "private-store-error"} {
				if strings.Contains(textOut.String()+jsonOut.String()+string(data), secret) {
					t.Fatalf("diagnostic leaked %q", secret)
				}
			}
			if after := snapshotDoctorTree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("receipt/journal state changed: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestDoctorStoredCodexQualificationReadOnlyArgvAndState(t *testing.T) {
	for _, name := range []string{"stored", "absent", "corrupt", "mismatch"} {
		t.Run(name, func(t *testing.T) {
			cmd, _, root := doctorQualificationFixture(t, name)
			bin := t.TempDir()
			ledger := filepath.Join(bin, "argv")
			t.Setenv("PATH", bin)
			t.Setenv("PROJMUX_QUALIFICATION_TEST_ARGV", ledger)
			for _, name := range []string{"codex", "projmux", "tmux", "kill", "pkill"} {
				script := "#!/bin/sh\nprintf '%s %s\\n' \"$0\" \"$*\" >> \"$PROJMUX_QUALIFICATION_TEST_ARGV\"\nexit 1\n"
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			// Keep a journal-shaped sentinel as well as the no-journal case
			// above: diagnostics must neither repair nor rewrite it.
			journalPath := codexupgrade.NewStateStore(filepath.Join(root, "state", "projmux")).Path()
			if err := os.MkdirAll(filepath.Dir(journalPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(journalPath, []byte("journal-do-not-repair"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotDoctorTree(t, root)
			for range 2 {
				for _, args := range [][]string{{"--section", "integrations"}, {"--section", "integrations", "--json"}} {
					if err := cmd.Run(args, io.Discard, io.Discard); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := (&diagnosticsCommand{doctor: cmd}).supportDoctorJSON(); err != nil {
					t.Fatal(err)
				}
			}
			if argv, err := os.ReadFile(ledger); !os.IsNotExist(err) || len(argv) != 0 {
				t.Fatalf("qualification diagnostic execution argv = %q, err=%v", argv, err)
			}
			if after := snapshotDoctorTree(t, root); !reflect.DeepEqual(before, after) {
				t.Fatalf("receipt/journal writes occurred: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestDoctorStoredCodexQualificationKeepsValidSiblingsVisible(t *testing.T) {
	cmd, store, _ := doctorQualificationFixture(t, "corrupt")
	receipt := *doctorQualifiedVersions()
	receipt.Versions = codexgeneration.VersionPair{Old: "0.154.0", New: "0.154.1"}
	if err := store.Save(receipt); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Dir(), "private-filename.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := cmd.codexQualification()
	if report.Status != "damaged" || len(report.VersionPairs) != 3 {
		t.Fatalf("mixed store report = %+v", report)
	}
	if report.VersionPairs[0].Status != "damaged" || report.VersionPairs[0].OldVersion != "0.153.2" ||
		report.VersionPairs[1].Status != "stored" || report.VersionPairs[1].OldVersion != "0.154.0" ||
		report.VersionPairs[1].Verdict != codexgeneration.VerdictYes || report.VersionPairs[2].Status != "damaged" ||
		report.VersionPairs[2].OldVersion != "" || report.VersionPairs[2].NewVersion != "" {
		t.Fatalf("mixed store lost a receipt or exposed an invalid filename: %+v", report.VersionPairs)
	}
}

func TestDoctorStoredCodexQualificationSupportRejectsNonDiagnosticStrings(t *testing.T) {
	for _, key := range []string{"status", "old_version", "new_version", "verdict", "reason"} {
		for _, value := range []string{"/private/payload", "private-name", "yes\nsecret", "0.153.4/private"} {
			row := map[string]any{key: value}
			redactDoctorJSON(row, "version_pairs")
			if row[key] == value {
				t.Fatalf("support preserved unsafe %s=%s", key, value)
			}
		}
	}
	if safeCodexQualificationString("other", "old_version", "0.153.4") {
		t.Fatal("qualification version exception escaped its scope")
	}
}
