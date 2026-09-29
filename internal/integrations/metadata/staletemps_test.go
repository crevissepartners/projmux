package metadata

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// staleTempFixture is one Registry directory seeded with a staged file of every
// family a killed write can leave, each once older and once younger than
// StaleTempAge, next to the neighbours reclaiming must never touch.
type staleTempFixture struct {
	stale []string
	kept  []string
}

func plantStaleTempFixture(t *testing.T, store *Store) staleTempFixture {
	t.Helper()
	dir := filepath.Dir(store.Path())
	recovery := store.recoveryDir
	if err := os.MkdirAll(recovery, 0o700); err != nil {
		t.Fatal(err)
	}
	old := 2 * localstate.StaleTempAge
	fresh := 10 * time.Second
	var f staleTempFixture
	plant := func(path string, age time.Duration, reclaimed bool) {
		t.Helper()
		if err := os.WriteFile(path, []byte("orphan\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(-age)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if reclaimed {
			f.stale = append(f.stale, path)
		} else {
			f.kept = append(f.kept, path)
		}
	}

	families := []string{
		filepath.Join(dir, ".registry.json.tmp-%d"),                                               // F1
		filepath.Join(dir, ".registry.initialized.tmp-%d"),                                        // F2
		filepath.Join(recovery, ".registry-20260801T000000Z-00.json.tmp-%d"),                      // F3
		filepath.Join(recovery, ".replaced-20260801T000000Z-03.json.tmp-%d"),                      // F4
		filepath.Join(dir, ".registry.json.v3.20260801T000000Z.bak.tmp-%d"),                       // F5 backup
		filepath.Join(dir, ".registry.json.v3.20260801T000000Z.bak.2.tmp-%d"),                     // F5 collision
		filepath.Join(dir, ".registry.json.v3.20260801T000000Z.bak.migration-report.json.tmp-%d"), // F5 report
	}
	for i, family := range families {
		plant(fmt.Sprintf(family, 1000+i), old, true)
		plant(fmt.Sprintf(family, 2000+i), fresh, false)
	}

	for _, name := range []string{
		"registry.json.v3.20260801T000000Z.bak",
		"registry.json.v3.20260801T000000Z.bak.migration-report.json",
		"registry.json.repair.lock",
		".registry.json.tmp-",
		".registry.json.tmp-abc",
		".registry.json.bak.tmp-1",
		".registry.json.v3.notastamp.bak.tmp-1",
		".registry.json.v3.20260801T000000Z.bak.x.tmp-1",
		".notes.tmp-1",
		"x.registry.json.tmp-1",
		"registry.json.tmp-1",
	} {
		plant(filepath.Join(dir, name), old, false)
	}
	for _, name := range []string{
		"registry-20260801T000000Z-00.json",
		"replaced-20260801T000000Z-00.json",
		".registry-bogus-00.json.tmp-1",
		".registry-20260801T000000Z-0.json.tmp-1",
		".registry.json.tmp-1",
		".notes.tmp-1",
	} {
		plant(filepath.Join(recovery, name), old, false)
	}
	return f
}

// recordStaleTempRemovals makes every reclaim removal observable.
func recordStaleTempRemovals(store *Store) *[]string {
	var removed []string
	store.hooks.removeStaleTemp = func(path string) error {
		removed = append(removed, path)
		return os.Remove(path)
	}
	return &removed
}

func assertPathsExist(t *testing.T, paths []string, want bool) {
	t.Helper()
	for _, path := range paths {
		_, err := os.Lstat(path)
		if got := err == nil; got != want {
			t.Errorf("%s exists = %v (err %v), want %v", path, got, err, want)
		}
	}
}

func sortedCopy(paths []string) []string {
	out := slices.Clone(paths)
	slices.Sort(out)
	return out
}

func seedRegistry(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.Update(func(*coremetadata.Registry) error { return nil }); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
}

func registerStaleTempProject(reg *coremetadata.Registry, root string) error {
	_, err := testMutator(map[string]bool{root: true}).RegisterProject(reg, coremetadata.RegisterProjectOptions{
		Root:         root,
		DefaultShell: "/bin/zsh",
		OperationID:  "op-" + filepath.Base(root),
	})
	return err
}

func TestCommittedRegistryWritesReclaimStaleTempsOfEveryStagedFamily(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		seed   func(*testing.T, *Store)
		commit func(*Store) error
	}{
		{
			name: "Update",
			seed: seedRegistry,
			commit: func(store *Store) error {
				_, err := store.Update(func(reg *coremetadata.Registry) error { return registerStaleTempProject(reg, "/src/projmux") })
				return err
			},
		},
		{
			name: "UpdateConvergent with a change",
			seed: seedRegistry,
			commit: func(store *Store) error {
				_, changed, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error { return registerStaleTempProject(reg, "/src/projmux") })
				if err == nil && !changed {
					err = errors.New("UpdateConvergent reported no change")
				}
				return err
			},
		},
		{
			name: "Migrate",
			seed: func(t *testing.T, store *Store) {
				writeRegistryFile(t, store, olderEnvelopeRegistry)
				withOlderEnvelopeStep(store)
			},
			commit: func(store *Store) error {
				result, err := store.Migrate()
				if err == nil && !result.Migrated {
					err = errors.New("Migrate did not migrate")
				}
				return err
			},
		},
		{
			name: "Load that migrates",
			seed: func(t *testing.T, store *Store) {
				writeRegistryFile(t, store, olderEnvelopeRegistry)
				withOlderEnvelopeStep(store)
			},
			commit: func(store *Store) error {
				_, result, err := store.LoadWithMigrationResult()
				if err == nil && !result.Migrated {
					err = errors.New("Load did not migrate")
				}
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			tc.seed(t, store)
			fixture := plantStaleTempFixture(t, store)
			removed := recordStaleTempRemovals(store)

			if err := tc.commit(store); err != nil {
				t.Fatalf("commit: %v", err)
			}

			if got, want := sortedCopy(*removed), sortedCopy(fixture.stale); !slices.Equal(got, want) {
				t.Fatalf("removed = %v, want exactly the stale temps %v", got, want)
			}
			assertPathsExist(t, fixture.stale, false)
			assertPathsExist(t, fixture.kept, true)
			assertPathsExist(t, []string{store.Path(), store.markerPath}, true)
		})
	}
}

func TestRegistryPathsThatCommitNoWriteReclaimNothing(t *testing.T) {
	t.Parallel()

	errRefused := errors.New("refused by the mutation callback")
	cases := []struct {
		name string
		run  func(*Store) error
	}{
		{"LoadReadOnly", func(store *Store) error { _, err := store.LoadReadOnly(); return err }},
		{"LoadSnapshot", func(store *Store) error { _, err := store.LoadSnapshot(); return err }},
		{"Load without migration", func(store *Store) error { _, err := store.Load(); return err }},
		{"Migrate no-op", func(store *Store) error { _, err := store.Migrate(); return err }},
		{"WithAdmissionBarrier", func(store *Store) error {
			return store.WithAdmissionBarrier(func(coremetadata.Registry) error { return nil })
		}},
		{"UpdateConvergent no-op", func(store *Store) error {
			_, changed, err := store.UpdateConvergent(func(*coremetadata.Registry) error { return nil })
			if err == nil && changed {
				err = errors.New("no-op convergence reported a change")
			}
			return err
		}},
		{"Update refused by its callback", func(store *Store) error {
			_, err := store.Update(func(*coremetadata.Registry) error { return errRefused })
			if errors.Is(err, errRefused) {
				return nil
			}
			return err
		}},
		{"Update rejected by validation", func(store *Store) error {
			_, err := store.Update(func(reg *coremetadata.Registry) error {
				reg.Projects = append(reg.Projects, coremetadata.Project{})
				return nil
			})
			if err == nil {
				return errors.New("an invalid registry was accepted")
			}
			return nil
		}},
		{"Update failing before the rename", func(store *Store) error {
			store.hooks.beforeRename = func() error { return errRefused }
			_, err := store.Update(func(reg *coremetadata.Registry) error { return registerStaleTempProject(reg, "/src/projmux") })
			store.hooks.beforeRename = nil
			if errors.Is(err, errRefused) {
				return nil
			}
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			seedRegistry(t, store)
			fixture := plantStaleTempFixture(t, store)
			removed := recordStaleTempRemovals(store)

			if err := tc.run(store); err != nil {
				t.Fatalf("run: %v", err)
			}

			if len(*removed) != 0 {
				t.Fatalf("removed = %v, want nothing after a path that committed no write", *removed)
			}
			assertPathsExist(t, fixture.stale, true)
			assertPathsExist(t, fixture.kept, true)
		})
	}
}

func TestStaleTempReclaimRunsWithTheRegistryLockReleased(t *testing.T) {
	t.Parallel()

	for _, observed := range []bool{false, true} {
		name := "unobserved"
		if observed {
			name = "observed"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			store := testStore(t)
			if observed {
				store.SetLockObserver(func(LockObservation) {})
			}
			seedRegistry(t, store)
			fixture := plantStaleTempFixture(t, store)
			checks := 0
			store.hooks.removeStaleTemp = func(path string) error {
				checks++
				probe, err := os.OpenFile(store.flockPath, os.O_RDWR, 0)
				if err != nil {
					t.Errorf("open registry flock: %v", err)
					return os.Remove(path)
				}
				defer func() { _ = probe.Close() }()
				if err := unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Errorf("reclaiming %s while the Registry flock is held: %v", filepath.Base(path), err)
				} else {
					_ = unix.Flock(int(probe.Fd()), unix.LOCK_UN)
				}
				if _, err := os.Lstat(store.lockPath); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("reclaiming %s while the legacy lock marker exists (err %v)", filepath.Base(path), err)
				}
				return os.Remove(path)
			}

			if _, err := store.Update(func(reg *coremetadata.Registry) error { return registerStaleTempProject(reg, "/src/projmux") }); err != nil {
				t.Fatalf("update: %v", err)
			}
			if checks != len(fixture.stale) {
				t.Fatalf("lock checks = %d, want one per stale temp (%d)", checks, len(fixture.stale))
			}
		})
	}
}

func TestStaleTempReclaimFailureDoesNotFailTheWrite(t *testing.T) {
	t.Parallel()

	store := testStore(t)
	seedRegistry(t, store)
	fixture := plantStaleTempFixture(t, store)
	var tried []string
	store.hooks.removeStaleTemp = func(path string) error {
		tried = append(tried, path)
		return os.ErrPermission
	}

	got, err := store.Update(func(reg *coremetadata.Registry) error { return registerStaleTempProject(reg, "/src/projmux") })
	if err != nil {
		t.Fatalf("update with unremovable temps: %v", err)
	}
	if len(got.Projects) != 1 {
		t.Fatalf("projects = %d, want the committed Project", len(got.Projects))
	}
	if len(tried) != len(fixture.stale) {
		t.Fatalf("tried = %v, want every stale temp attempted once", tried)
	}
	assertPathsExist(t, fixture.stale, true)
	loaded, err := store.Load()
	if err != nil || len(loaded.Projects) != 1 {
		t.Fatalf("reload = %d projects, err %v; want the committed write", len(loaded.Projects), err)
	}

	store.hooks.removeStaleTemp = nil
	if _, err := store.Update(func(reg *coremetadata.Registry) error { return registerStaleTempProject(reg, "/src/other") }); err != nil {
		t.Fatalf("second update: %v", err)
	}
	assertPathsExist(t, fixture.stale, false)
}
