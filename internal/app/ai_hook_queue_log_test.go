package app

import (
	"encoding/json"
	"os"
	"testing"
)

// hookLogRecords reads the ingest log as raw JSON objects, so a test pins the
// wire fields an operator reads rather than the Go struct.
func hookLogRecords(t *testing.T, cmd *aiCommand) []map[string]any {
	t.Helper()
	path, err := cmd.aiIngestLogPath()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ingest log is unreadable: %v", err)
	}
	var records []map[string]any
	for _, line := range nonEmptyLines(string(data)) {
		record := map[string]any{}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("ingest log line %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

// notifyRecord returns the one `notify` record the hook wrote.
func notifyRecord(t *testing.T, records []map[string]any) map[string]any {
	t.Helper()
	var found []map[string]any
	for _, record := range records {
		if record["result"] == "notify" {
			found = append(found, record)
		}
	}
	if len(found) != 1 {
		t.Fatalf("notify records = %d, want 1: %v", len(found), records)
	}
	return found[0]
}

// A hook that queued an entry says so on its `notify` record, and the result
// word and the record's other fields are the ones the report already reads.
func TestHookNotifyLogSaysQueuedForUnmarkedAppOwnedShellPane(t *testing.T) {
	for _, tc := range unmarkedShellPayloads {
		t.Run(tc.name, func(t *testing.T) {
			cmd, store := unmarkedShellCommand(t, "%7", unmarkedShellOptions{appMarker: "1", paneUID: "pane-shell"})
			unmarkedShellHookRun(t, cmd, tc.provider, tc.payload)
			if len(store.pushed) != 1 {
				t.Fatalf("queue pushes = %d, want 1", len(store.pushed))
			}
			record := notifyRecord(t, hookLogRecords(t, cmd))
			if record["queue"] != "queued" {
				t.Errorf("queue = %v, want queued: %v", record["queue"], record)
			}
			if _, ok := record["queue_reason"]; ok {
				t.Errorf("a queued record carries a queue_reason: %v", record)
			}
			if record["source"] != tc.provider+"-hook" || record["pane"] != "%7" {
				t.Errorf("existing fields changed: %v", record)
			}
		})
	}
}

// A Pane with its own provider marker queues, and the record says so.
func TestHookNotifyLogSaysQueuedForMarkedPane(t *testing.T) {
	cmd, store := unmarkedShellCommand(t, "%7", unmarkedShellOptions{agent: "claude"})
	unmarkedShellHookRun(t, cmd, "claude", unmarkedShellPayloads[0].payload)
	if len(store.pushed) != 1 {
		t.Fatalf("queue pushes = %d, want 1", len(store.pushed))
	}
	if record := notifyRecord(t, hookLogRecords(t, cmd)); record["queue"] != "queued" {
		t.Fatalf("queue = %v, want queued: %v", record["queue"], record)
	}
}

// A hook that queued nothing keeps `result` at notify and names the guard that
// stopped it; each guard has its own token, so the log line alone tells a
// foreign server from a Pane the app never gave a UID.
func TestHookNotifyLogNamesTheGuardWhenNothingIsQueued(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opts   unmarkedShellOptions
		reason string
	}{
		{"foreign-standalone-socket", unmarkedShellOptions{appMarker: "", paneUID: "pane-shell"}, "not_app_owned"},
		{"app-pane-without-uid", unmarkedShellOptions{appMarker: "1"}, "no_pane_uid"},
	} {
		for _, p := range unmarkedShellPayloads {
			t.Run(tc.name+"/"+p.name, func(t *testing.T) {
				cmd, store := unmarkedShellCommand(t, "%7", tc.opts)
				unmarkedShellHookRun(t, cmd, p.provider, p.payload)
				if len(store.pushed) != 0 {
					t.Fatalf("queued %d entries", len(store.pushed))
				}
				record := notifyRecord(t, hookLogRecords(t, cmd))
				if record["queue"] != "not_queued" || record["queue_reason"] != tc.reason {
					t.Errorf("queue = %v reason = %v, want not_queued %s: %v", record["queue"], record["queue_reason"], tc.reason, record)
				}
			})
		}
	}
}
