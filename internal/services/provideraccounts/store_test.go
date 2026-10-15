package provideraccounts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	account "github.com/crevissepartners/projmux/internal/core/provideraccounts"
	"github.com/crevissepartners/projmux/internal/state"
)

func TestCatalogKernelLockCancelAndTimeout(t *testing.T) {
	s, p := fixtureService(t)
	ctx := context.Background()
	a, err := s.Register(ctx, account.RegisterAccountRequest{Name: "one", Provider: "codex", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: p.ProviderAccountsFile()}
	held, err := state.AcquireFileLock(store.lockPath(), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	before, _ := os.ReadFile(store.Path)
	contended := make(chan struct{})
	store.afterContended = func() { close(contended) }
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- store.update(cctx, func(c *catalog) (bool, error) { t.Error("canceled waiter admitted"); return false, nil })
	}()
	select {
	case <-contended:
	case <-time.After(3 * time.Second):
		t.Fatal("no contention")
	}
	cancel()
	requireReason(t, <-done, account.Canceled)
	store.afterContended = nil
	store.lockLimit = 10 * time.Millisecond
	err = store.update(ctx, func(c *catalog) (bool, error) { t.Error("timeout waiter admitted"); return false, nil })
	requireReason(t, err, account.LockTimeout)
	after, _ := os.ReadFile(store.Path)
	if string(before) != string(after) {
		t.Fatal("lock failure changed catalog")
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Remove(ctx, account.RemoveAccountRequest{Ref: "uid:" + a.UID}); err != nil {
		t.Fatal("late grant leaked lock", err)
	}
}
func TestCatalogAtomicFaultAndPrivateModes(t *testing.T) {
	s, p := fixtureService(t)
	ctx := context.Background()
	_, err := s.Register(ctx, account.RegisterAccountRequest{Name: "one", Provider: "codex", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: p.ProviderAccountsFile()}
	before, _ := os.ReadFile(store.Path)
	fault := errors.New("private filesystem value")
	change := func(c *catalog) (bool, error) { c.Records[0].Account.State = account.Removed; return true, nil }
	store.beforeReplace = func() error { return fault }
	err = store.update(ctx, change)
	requireReason(t, err, account.IOFailure)
	after, _ := os.ReadFile(store.Path)
	if string(before) != string(after) || strings.Contains(err.Error(), fault.Error()) {
		t.Fatal("precommit fault effect/leak")
	}
	store.beforeReplace = nil
	cctx, cancel := context.WithCancel(ctx)
	store.beforeReplace = func() error { cancel(); return nil }
	err = store.update(cctx, change)
	requireReason(t, err, account.Canceled)
	after, _ = os.ReadFile(store.Path)
	if string(before) != string(after) {
		t.Fatal("precommit cancellation effect")
	}
	store.beforeReplace = nil
	store.syncDir = func(string) error { return fault }
	err = store.update(ctx, change)
	requireReason(t, err, account.Unknown)
	c, err := store.read()
	if err != nil || c.Records[0].Account.State != account.Removed {
		t.Fatal("unknown outcome did not retain committed state", err)
	}
	for _, path := range []string{store.Path, store.lockPath()} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("private file %s %v %v", path, st, err)
		}
	}
	st, err := os.Stat(p.ConfigDir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatal("private directory", st, err)
	}
	temps, _ := filepath.Glob(filepath.Join(p.ConfigDir, ".provider-accounts-*"))
	if len(temps) != 0 {
		t.Fatal("temporary files remain", temps)
	}
}
func TestCatalogRejectsInvalidStoredRecords(t *testing.T) {
	s, p := fixtureService(t)
	_, err := s.Register(context.Background(), account.RegisterAccountRequest{Name: "work", Provider: "codex", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p.ProviderAccountsFile())
	for _, edit := range []func(*catalog){func(c *catalog) { c.Version = 2 }, func(c *catalog) { c.Records = nil }, func(c *catalog) { c.Records[0].Account.AuthenticationStatus = "verified" }, func(c *catalog) { c.Records[0].Account.Version = 0 }, func(c *catalog) { c.Records[0].Account.UID = "bad" }, func(c *catalog) { c.Records[0].Proof.Inode = 0 }, func(c *catalog) { c.Records = append(c.Records, c.Records[0]) }} {
		var c catalog
		_ = json.Unmarshal(raw, &c)
		edit(&c)
		data, _ := json.Marshal(c)
		if err = os.WriteFile(p.ProviderAccountsFile(), data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = s.List(context.Background())
		requireReason(t, err, account.StoreInvalid)
	}
	for _, data := range []string{string(raw) + "{}", strings.Replace(string(raw), `"version": 1`, `"secret": "inert", "version": 1`, 1)} {
		if err = os.WriteFile(p.ProviderAccountsFile(), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = s.List(context.Background())
		requireReason(t, err, account.StoreInvalid)
	}
}

// This helper is the test binary itself, not a provider, CLI, or native process.
func TestCatalogWriterProcess(t *testing.T) {
	if os.Getenv("PROJMUX_ACCOUNT_TEST_WORKER") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	p := config.Paths{ConfigDir: os.Getenv("PROJMUX_ACCOUNT_TEST_DIR")}
	name := os.Getenv("PROJMUX_ACCOUNT_TEST_NAME")
	root := os.Getenv("PROJMUX_ACCOUNT_TEST_ROOT")
	proof, err := directoryProof(root)
	if err != nil {
		t.Fatalf("root proof: %v", err)
	}
	// This is a Store retention test. Service admission and the default 500ms
	// lock budget have separate coverage; disk/scheduler speed is not its oracle.
	store := Store{Path: p.ProviderAccountsFile(), lockLimit: 30 * time.Second}
	store.afterContended = func() {
		if err := os.WriteFile(filepath.Join(p.ConfigDir, name+".ready"), nil, 0o600); err != nil {
			t.Fatalf("contention ready: %v", err)
		}
	}
	uid := "account-" + strings.Repeat("0", 31) + strings.TrimPrefix(name, "worker-")
	err = store.update(ctx, func(c *catalog) (bool, error) {
		c.Records = append(c.Records, record{Account: account.ProviderAccount{
			UID: uid, Name: name, Provider: "codex", Root: root,
			Version: account.RegistrationVersion, State: account.Registered,
			AuthenticationStatus: account.Unverified,
		}, Proof: proof})
		return true, nil
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// Only the first contention needs to signal readiness.
	store.afterContended = nil
	err = store.update(ctx, func(c *catalog) (bool, error) {
		for i := range c.Records {
			if c.Records[i].Account.UID == uid {
				c.Records[i].Account.State = account.Removed
				return true, nil
			}
		}
		return false, fmt.Errorf("registered row %s missing", uid)
	})
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
}

func TestCatalogMultiProcessWritersLoseNoRows(t *testing.T) {
	p := config.Paths{ConfigDir: filepath.Join(t.TempDir(), "config")}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := Store{Path: p.ProviderAccountsFile()}
	held, err := state.AcquireFileLockContext(ctx, store.lockPath(), time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	type writer struct {
		cmd    *exec.Cmd
		output bytes.Buffer
		done   chan struct{}
		err    error
	}
	roots := t.TempDir()
	writers := make([]*writer, 0, n)
	// Cleanup is installed before the first Start: cancellation kills only our
	// children, and every successful Start has exactly one actual Wait.
	t.Cleanup(func() {
		cancel()
		_ = held.Close()
		for i, w := range writers {
			<-w.done
			t.Logf("writer %d PID %d Wait=%v\n%s", i, w.cmd.Process.Pid, w.err, w.output.String())
		}
	})
	for i := range n {
		root := filepath.Join(roots, fmt.Sprintf("worker-%d", i))
		if err = os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		w := &writer{done: make(chan struct{})}
		w.cmd = exec.CommandContext(ctx, exe, "-test.run=^TestCatalogWriterProcess$", "-test.v")
		w.cmd.WaitDelay = time.Second
		w.cmd.Stdout, w.cmd.Stderr = &w.output, &w.output
		w.cmd.Env = append(os.Environ(), "PROJMUX_ACCOUNT_TEST_WORKER=1", "PROJMUX_ACCOUNT_TEST_DIR="+p.ConfigDir, "PROJMUX_ACCOUNT_TEST_NAME="+fmt.Sprintf("worker-%d", i), "PROJMUX_ACCOUNT_TEST_ROOT="+root)
		if err = w.cmd.Start(); err != nil {
			t.Fatalf("start writer %d: %v\n%s", i, err, w.output.String())
		}
		writers = append(writers, w)
		go func() {
			w.err = w.cmd.Wait()
			close(w.done)
		}()
	}
	// All eight processes must encounter the held kernel lock before release.
	// This proves real contention rather than relying on process launch timing.
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for i, w := range writers {
		ready := filepath.Join(p.ConfigDir, fmt.Sprintf("worker-%d.ready", i))
		for {
			if _, err = os.Stat(ready); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("writer %d readiness: %v", i, err)
			}
			select {
			case <-w.done:
				t.Fatalf("writer %d exited before contention: %v\n%s", i, w.err, w.output.String())
			case <-ctx.Done():
				t.Fatalf("waiting for writer %d contention: %v", i, ctx.Err())
			case <-ticker.C:
				continue
			}
		}
	}
	if err = held.Close(); err != nil {
		t.Fatal(err)
	}
	for i, w := range writers {
		<-w.done
		if w.err != nil {
			t.Errorf("writer %d PID %d Wait=%v\n%s", i, w.cmd.Process.Pid, w.err, w.output.String())
		}
	}
	c, err := store.read()
	if err != nil || len(c.Records) != n {
		t.Fatalf("rows %d %v", len(c.Records), err)
	}
	seen := make(map[string]bool, n)
	for _, r := range c.Records {
		if r.Account.State != account.Removed {
			t.Errorf("retirement lost: %s", r.Account.Name)
		}
		seen[r.Account.Name] = true
	}
	for i := range n {
		if name := fmt.Sprintf("worker-%d", i); !seen[name] {
			t.Errorf("writer row missing: %s", name)
		}
	}
}

func TestCatalogRenameUncertaintyAndLateCancellation(t *testing.T) {
	s, p := fixtureService(t)
	ctx := context.Background()
	_, err := s.Register(ctx, account.RegisterAccountRequest{Name: "work", Provider: "codex", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Path: p.ProviderAccountsFile()}
	change := func(c *catalog) (bool, error) { c.Records[0].Account.State = account.Removed; return true, nil }
	before, _ := os.ReadFile(store.Path)
	store.replace = func(string, string) error { return context.Canceled }
	err = store.update(ctx, change)
	requireReason(t, err, account.Unknown)
	after, _ := os.ReadFile(store.Path)
	if string(before) != string(after) {
		t.Fatal("failed replacement modified catalog")
	}
	store.replace = nil
	late, cancel := context.WithCancel(ctx)
	store.syncDir = func(path string) error { cancel(); return syncDirectory(path) }
	if err = store.update(late, change); err != nil {
		t.Fatal("late cancellation misreported commit", err)
	}
	c, err := store.read()
	if err != nil || c.Records[0].Account.State != account.Removed {
		t.Fatal("late cancellation lost commit", err)
	}
}
func TestCatalogConcurrentDuplicateNameHasOneWinner(t *testing.T) {
	s, _ := fixtureService(t)
	const n = 8
	start := make(chan struct{})
	results := make(chan error, n)
	for range n {
		root := t.TempDir()
		go func() {
			<-start
			_, err := s.Register(context.Background(), account.RegisterAccountRequest{Name: "same", Provider: "codex", Root: root})
			results <- err
		}()
	}
	close(start)
	wins := 0
	for range n {
		if err := <-results; err == nil {
			wins++
		} else {
			requireReason(t, err, account.NameDuplicate)
		}
	}
	rows, err := s.List(context.Background())
	if err != nil || wins != 1 || len(rows) != 1 {
		t.Fatalf("duplicate race wins=%d rows=%v err=%v", wins, rows, err)
	}
}
func TestCatalogRejectsSymlinkedStoreWithoutReadingTarget(t *testing.T) {
	s, p := fixtureService(t)
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "inert")
	if err := os.WriteFile(target, []byte("inert private text"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, p.ProviderAccountsFile()); err != nil {
		t.Fatal(err)
	}
	_, err := s.List(context.Background())
	requireReason(t, err, account.StoreInvalid)
	_, err = s.Register(context.Background(), account.RegisterAccountRequest{Name: "work", Provider: "codex", Root: t.TempDir()})
	requireReason(t, err, account.StoreInvalid)
	data, _ := os.ReadFile(target)
	if string(data) != "inert private text" {
		t.Fatal("target overwritten")
	}
}
