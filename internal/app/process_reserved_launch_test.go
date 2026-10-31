package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

func reservedLaunchFixture(t *testing.T) (registryPath, path string, record createdLaunchRecord) {
	t.Helper()
	root := t.TempDir()
	registryPath = filepath.Join(root, "state", "projmux", "registry.json")
	path = mutationruntime.CreatedLaunchPath(registryPath, "agent-one")
	record = createdLaunchRecord{Version: reservedLaunchVersion, Binding: processhost.Binding{Agent: "agent-one", Pane: "pane-one", Generation: "gen-one"}, Provider: "claude", Command: processhost.Command{Path: "/bin/true", Args: []string{"--print"}}}
	return registryPath, path, record
}

func TestCreatedLaunchIsPrivateAndRoundTrips(t *testing.T) {
	_, path, record := reservedLaunchFixture(t)
	if err := writeCreatedLaunch(path, record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	got, err := readCreatedLaunch(path)
	if err != nil || got == nil || got.Binding != record.Binding || got.Command.Path != record.Command.Path {
		t.Fatalf("round trip %+v %v", got, err)
	}
}

func TestCreatedLaunchRefusesPastItsBound(t *testing.T) {
	_, path, record := reservedLaunchFixture(t)
	record.Command.Env = []string{"BIG=" + strings.Repeat("x", 70000)}
	err := writeCreatedLaunch(path, record)
	if !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatalf("oversized launch: %v, want a refusal", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused write left a file: %v", statErr)
	}
}

func TestCreatedLaunchDamagedOrUnsafeIsRefused(t *testing.T) {
	_, path, record := reservedLaunchFixture(t)
	if err := writeCreatedLaunch(path, record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCreatedLaunch(path); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatalf("damaged launch: %v, want a refusal", err)
	}
	if err := os.WriteFile(path, mustJSON(t, record), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCreatedLaunch(path); !errors.Is(err, processhost.ErrResumeRefused) {
		t.Fatalf("group-readable launch: %v, want a refusal", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A binary that predates created-launches never reads or changes its file.
func TestDeferredLaunchReaderIgnoresCreatedLaunches(t *testing.T) {
	registryPath, path, record := reservedLaunchFixture(t)
	if err := writeCreatedLaunch(path, record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &agentCommand{}
	c.messagePaths.registryPath = registryPath
	got, err := c.readDeferredLaunch("agent-one")
	if err != nil || got != nil {
		t.Fatalf("deferred reader saw the reservation: %+v %v", got, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("deferred reader changed the reservation file: %v", err)
	}
}

// Only a reservation request carries Reserve, so an executable that rejects
// unknown fields still accepts every other request and refuses this one.
func TestReserveWireFieldIsOmittedUnlessSet(t *testing.T) {
	plain := mustJSON(t, processDetachedRequest{})
	if bytes.Contains(plain, []byte("Reserve")) {
		t.Fatalf("a request without Reserve carries the field: %s", plain)
	}
	reserve := mustJSON(t, processDetachedRequest{Reserve: true})
	var old struct {
		RelaunchContext *mutationruntime.ProcessCreateContext
		Relaunch        *processDetachedRelaunchRequest
		Resume          *processDetachedResumeRequest
		Options         processAgentCreateOptions
		Output          json.RawMessage
		Caller          json.RawMessage
		RegistryPath    string
		Context         *mutationruntime.ProcessCreateContext
		ResumeContext   *mutationruntime.ProcessCreateContext
	}
	decoder := json.NewDecoder(bytes.NewReader(reserve))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&old); err == nil || !strings.Contains(err.Error(), "Reserve") {
		t.Fatalf("a decoder without Reserve accepted a reservation request: %v", err)
	}
	runtimePlain := mustJSON(t, mutationruntime.ProcessDetachedCreateRequest{})
	if bytes.Contains(runtimePlain, []byte("Reserve")) {
		t.Fatalf("a runtime create request carries Reserve: %s", runtimePlain)
	}
}
