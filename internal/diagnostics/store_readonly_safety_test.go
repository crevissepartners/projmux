package diagnostics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreReadOnlyRejectsOversize(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, LogFileName)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, MaxLogSize+1); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).ReadOnly(); !errors.Is(err, errReadOnlyLimit) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestStoreReadOnlyAndRuntimeHealthAcceptMoreThan4096ValidEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), LogFileName)
	var body []byte
	for index := range 4097 {
		record, err := json.Marshal(fixtureEvent(fmt.Sprintf("run-%04d", index)))
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, record...)
		body = append(body, '\n')
	}
	if len(body) >= MaxLogSize {
		t.Fatalf("fixture size = %d, want < %d", len(body), MaxLogSize)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := NewStore(path).ReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 4097 || result.Malformed != 0 || result.Truncated {
		t.Fatalf("read result = events:%d malformed:%d truncated:%v", len(result.Events), result.Malformed, result.Truncated)
	}
	health, err := ReadRuntimeHealth(NewStore(path))
	if err != nil {
		t.Fatal(err)
	}
	if health.Missing || health.Malformed != 0 || health.Truncated || health.RecentErrorCount != 0 {
		t.Fatalf("runtime health = %#v", health)
	}
}
