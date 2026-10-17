package agentguidance

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestLegacyMigrationLoadPreservesUserAndBackup(t *testing.T) {
	entries, err := os.ReadDir("legacy")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			old, err := os.ReadFile(filepath.Join("legacy", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			user := []byte("\n# User decisions\r\nKeep these exact bytes.\n")
			original := append(bytes.Clone(old), user...)
			original = append(original, []byte("<!-- projmux-web:artifacts:begin -->\napp text\n<!-- projmux-web:artifacts:end -->")...)
			store := newTestStore(t)
			if err := store.Save(original); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(store.Path(), 0640); err != nil {
				t.Fatal(err)
			}
			got, err := store.Load()
			if err != nil {
				t.Fatal(err)
			}
			content, _ := os.ReadFile(store.Path())
			backup, _ := os.ReadFile(store.Path() + ".bak")
			if !bytes.Equal(content, user) || !bytes.Equal(backup, original) {
				t.Fatalf("migration content=%q backup=%q", content, backup)
			}
			for _, path := range []string{store.Path(), store.Path() + ".bak"} {
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0640 {
					t.Fatal("mode changed")
				}
			}
			again, err := store.Load()
			if err != nil || !bytes.Equal(got.Text, again.Text) {
				t.Fatalf("second load: %v", err)
			}
		})
	}
}

func TestLegacyMigrationConcurrentAndConflictingBackup(t *testing.T) {
	old, err := os.ReadFile("legacy/c4fc721e.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent", true: "conflict"}[conflict], func(t *testing.T) {
			store := newTestStore(t)
			original := append(bytes.Clone(old), []byte("USER")...)
			if err := store.Save(original); err != nil {
				t.Fatal(err)
			}
			if conflict {
				if err := os.WriteFile(store.Path()+".bak", []byte("other backup"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var wg sync.WaitGroup
			var notices atomic.Int32
			for range 8 {
				wg.Go(func() {
					guidance, err := store.Load()
					if guidance.MigrationNotice != "" {
						notices.Add(1)
					}
					if conflict {
						if err == nil {
							t.Error("conflicting backup accepted")
						}
					} else if err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			if !conflict && notices.Load() != 1 {
				t.Fatalf("concurrent migration notices=%d", notices.Load())
			}
			user, _ := os.ReadFile(store.Path())
			backup, _ := os.ReadFile(store.Path() + ".bak")
			if conflict {
				if !bytes.Equal(user, original) || string(backup) != "other backup" {
					t.Fatal("conflict lost data")
				}
			} else if string(user) != "USER" || !bytes.Equal(backup, original) {
				t.Fatal("concurrent migration lost data")
			}
		})
	}
}

func TestLegacyMigrationUncertainTextIsPreserved(t *testing.T) {
	store := newTestStore(t)
	original := []byte("# Working with other agents through projmux\nEdited old default\n<!-- projmux-web:artifacts:begin -->\nIncomplete\n")
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	content, _ := os.ReadFile(store.Path())
	if !bytes.Equal(content, original) || !strings.Contains(string(got.Text), "Edited old default") || !strings.Contains(got.MigrationNotice, "unrecognized prefix, markers") {
		t.Fatal("uncertain text removed")
	}
}

func TestLegacyMigrationFailureRetryAndNotice(t *testing.T) {
	for _, step := range []string{"after-backup", "after-prepare", "after-policy", "after-user", "before-complete"} {
		t.Run(step, func(t *testing.T) {
			store := newTestStore(t)
			old, _ := os.ReadFile("legacy/c4fc721e.md")
			original := append(bytes.Clone(old), []byte("USER\r\n")...)
			if err := store.Save(original); err != nil {
				t.Fatal(err)
			}
			store.migrationStep = func(at string) error {
				if at == step {
					return os.ErrPermission
				}
				return nil
			}
			if _, err := store.Load(); err == nil {
				t.Fatal("injected failure accepted")
			}
			backup, err := os.ReadFile(store.Path() + ".bak")
			if err != nil || !bytes.Equal(backup, original) {
				t.Fatal("backup lost on failure")
			}
			current, _ := os.ReadFile(store.Path())
			if !bytes.Equal(current, original) && string(current) != "USER\r\n" {
				t.Fatal("partial user write")
			}
			store.migrationStep = nil
			got, err := store.Load()
			if err != nil || !strings.Contains(got.MigrationNotice, "removed historical default prefix=true") {
				t.Fatalf("retry notice=%q error=%v", got.MigrationNotice, err)
			}
			again, err := store.Load()
			if err != nil || again.MigrationNotice != "" {
				t.Fatal("repeated notice")
			}
			current, _ = os.ReadFile(store.Path())
			if string(current) != "USER\r\n" {
				t.Fatal("retry damaged user text")
			}
		})
	}
}

func TestLegacyMigrationSettingsAndPolicyPreservation(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy off", true: "explicit on"}[explicit], func(t *testing.T) {
			store := newTestStore(t)
			if err := store.Save([]byte(" \r\n\t")); err != nil {
				t.Fatal(err)
			}
			if explicit {
				if err := store.SaveSettings(Settings{DisabledFragments: []string{"z.md"}}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := store.Load()
			if err != nil || got.Off() == explicit {
				t.Fatal("legacy off or explicit on changed", err)
			}
			settings, present, err := store.LoadSettings()
			receipt, receiptErr := store.loadMigration()
			if err != nil || !present || settings.Disabled == explicit || receiptErr != nil || receipt == nil || !receipt.Complete {
				t.Fatal("missing durable policy", err)
			}
			if explicit && len(settings.DisabledFragments) != 1 {
				t.Fatal("lost fragment mask")
			}
			if err := store.SetEnabled("a.md", false); err != nil {
				t.Fatal(err)
			}
			settings, _, _ = store.LoadSettings()
			if settings.Disabled == explicit {
				t.Fatal("fragment control toggled whole guidance")
			}
			if err := store.SetEnabled("", true); err != nil {
				t.Fatal(err)
			}
			settings, _, _ = store.LoadSettings()
			if settings.Disabled || len(settings.DisabledFragments) == 0 {
				t.Fatal("whole enable lost mask")
			}
			content, _ := os.ReadFile(store.Path())
			if string(content) != " \r\n\t" {
				t.Fatal("enable removed user content")
			}
		})
	}
}

func TestLegacyMigrationMarkerSafetyAndArchivePins(t *testing.T) {
	for _, input := range []string{
		webBegin + "\npartial", webEnd + "\nuser", webBegin + "\n" + webBegin + "\nbody\n" + webEnd + "\n" + webEnd + "\n", "inline " + webBegin + "\nbody\n" + webEnd,
	} {
		got, _, blocks := cleanLegacy([]byte(input))
		if string(got) != input || blocks != 0 {
			t.Fatalf("unsafe marker removal %q", input)
		}
	}
	input := "before\r\n" + webBegin + "\r\nbody\r\n" + webEnd + "\r\nafter\n"
	got, _, blocks := cleanLegacy([]byte(input))
	if string(got) != "before\r\nafter\n" || blocks != 1 {
		t.Fatalf("complete block: %q", got)
	}
	pins := map[string]string{"c4fc721e.md": "90cf07f1d32c8cf34a498c918d22244ff985a68af6226f1e9bdb8d9524c6fad0", "c79edd8f.md": "6e6cdf722f44cac921754e88a724351f1ebf4e2d616b2f21a1023a54c2e3594b", "002aad88.md": "88ebbad0d7e81d7968ed3391153abbb1073cfe82eed760cf57c57a2078979306"}
	entries, _ := legacyDefaults.ReadDir("legacy")
	if len(entries) != len(pins) {
		t.Fatal("archive entries changed")
	}
	for name, digest := range pins {
		text, _ := legacyDefaults.ReadFile("legacy/" + name)
		if Digest(text) != digest {
			t.Fatal("historical Default bytes changed:", name)
		}
	}
}

func TestLegacyMigrationPreservesConcurrentUserEdit(t *testing.T) {
	store := newTestStore(t)
	old, _ := os.ReadFile("legacy/c4fc721e.md")
	if err := store.Save(append(old, []byte("USER")...)); err != nil {
		t.Fatal(err)
	}
	store.migrationStep = func(step string) error {
		if step == "after-prepare" {
			return os.WriteFile(store.Path(), []byte("USER EDIT"), 0600)
		}
		return nil
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("concurrent user edit was not refused")
	}
	current, _ := os.ReadFile(store.Path())
	if string(current) != "USER EDIT" {
		t.Fatal("concurrent user edit overwritten")
	}
}

func TestLegacyMigrationActualWriteFailureRetry(t *testing.T) {
	store := newTestStore(t)
	old, _ := os.ReadFile("legacy/c4fc721e.md")
	original := append(old, []byte("USER")...)
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	// A directory in place of the receipt destination forces the real atomic
	// rename to fail. Keep the prepared receipt intact to restore on retry.
	store.migrationStep = func(step string) error {
		if step == "after-user" {
			if err := os.Rename(store.migrationPath(), store.migrationPath()+".prepared"); err != nil {
				return err
			}
			return os.Mkdir(store.migrationPath(), 0700)
		}
		return nil
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("completion write unexpectedly succeeded")
	}
	if err := os.Remove(store.migrationPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(store.migrationPath()+".prepared", store.migrationPath()); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.loadMigration()
	if err != nil || receipt == nil || receipt.Complete {
		t.Fatal("prepared receipt was lost", err)
	}
	store.migrationStep = nil
	got, err := store.Load()
	if err != nil || got.MigrationNotice == "" {
		t.Fatal("retry failed", err)
	}
	backup, _ := os.ReadFile(store.Path() + ".bak")
	current, _ := os.ReadFile(store.Path())
	if !bytes.Equal(backup, original) || string(current) != "USER" {
		t.Fatal("retry lost data")
	}
	entries, err := os.ReadDir(store.configDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") || strings.Contains(entry.Name(), ".backup-") {
			t.Fatal("temporary residue")
		}
	}
}

func TestGuidancePolicyUpdatesKeepMigrationReceiptAndMasks(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save([]byte("USER")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, name := range []string{"a.md", "b.md", "c.md", "d.md"} {
		wg.Go(func() {
			if err := store.SetEnabled(name, false); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	settings, _, err := store.LoadSettings()
	receipt, receiptErr := store.loadMigration()
	if err != nil || len(settings.DisabledFragments) != 4 || receiptErr != nil || receipt == nil || !receipt.Complete {
		t.Fatal("lost concurrent mask update", err)
	}
	if err := store.SaveSettings(Settings{}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save([]byte("EDITED USER")); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil || got.MigrationNotice != "" || !strings.HasSuffix(string(got.Text), "EDITED USER") {
		t.Fatal("settings API reset migration receipt", err)
	}
}

func TestGuidanceControlsDoNotMigrateUserFile(t *testing.T) {
	for _, fragment := range []string{"", "a.md"} {
		store := newTestStore(t)
		old, _ := os.ReadFile("legacy/c4fc721e.md")
		original := append(old, []byte("USER")...)
		if err := store.Save(original); err != nil {
			t.Fatal(err)
		}
		if err := store.SetEnabled(fragment, false); err != nil {
			t.Fatal(err)
		}
		current, _ := os.ReadFile(store.Path())
		if !bytes.Equal(current, original) {
			t.Fatal("policy control migrated user content")
		}
		if _, err := os.Stat(store.Path() + ".bak"); !os.IsNotExist(err) {
			t.Fatal("policy control created migration backup")
		}
		receipt, err := store.loadMigration()
		if err != nil || receipt != nil {
			t.Fatal("policy control ran migration", err)
		}
	}
}

func TestFragmentControlsPreserveLegacyOffBeforeFirstLoad(t *testing.T) {
	store := newTestStore(t)
	original := []byte(" \r\n")
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEnabled("a.md", false); err != nil {
		t.Fatal(err)
	}
	settings, _, err := store.LoadSettings()
	receipt, receiptErr := store.loadMigration()
	if err != nil || !settings.Disabled || receiptErr != nil || receipt != nil {
		t.Fatal("fragment policy lost legacy off", err)
	}
	if err := store.SetEnabled("a.md", true); err != nil {
		t.Fatal(err)
	}
	settings, _, err = store.LoadSettings()
	if err != nil || !settings.Disabled {
		t.Fatal("fragment enable toggled global off", err)
	}
	current, _ := os.ReadFile(store.Path())
	if !bytes.Equal(current, original) {
		t.Fatal("control changed blank user bytes")
	}
}

func TestSyncDirectoryAcceptsOnlyDirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "regular")
	if err := os.WriteFile(file, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := syncDirectory(file); err == nil {
		t.Fatal("directory sync accepted a regular file")
	}
	if err := syncDirectory(dir); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "preserve" {
		t.Fatal("directory sync changed file content", err)
	}
}
