package agentguidance

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func TestUserGuidanceKeepsLatestDefaultInSnapshot(t *testing.T) {
	store := newTestStore(t)
	user := []byte("# Decisions and approvals\nAsk through the question tool.\n")
	if err := store.Save(user); err != nil {
		t.Fatal(err)
	}
	g, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	snap, err := store.WriteSnapshot(g.Text)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(snap.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := append(Default(), append([]byte("\n\n"), user...)...)
	if !bytes.Equal(content, want) || !bytes.Contains(content, []byte("projmux config agent-host")) {
		t.Fatal("user file replaced the latest built-in guidance in the snapshot")
	}
}

func putFragment(t *testing.T, store Store, name, text string) string {
	t.Helper()
	dir := filepath.Join(store.configDir, FragmentsDirName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFragmentAssemblyOrderAndProcessSnapshot(t *testing.T) {
	store := newTestStore(t)
	putFragment(t, store, "z.md", " Z\n")
	putFragment(t, store, "10.md", "10\r\n")
	putFragment(t, store, "A.md", "A")
	putFragment(t, store, "empty.md", "")
	putFragment(t, store, "ignored.MD", "wrong extension")
	putFragment(t, store, "ignored.txt", "wrong extension")
	dir := filepath.Join(store.configDir, FragmentsDirName)
	if err := os.Mkdir(filepath.Join(dir, "directory.md"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("A.md", filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe.md"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.Save([]byte(" user\n")); err != nil {
		t.Fatal(err)
	}
	for _, process := range []bool{false, true} {
		g, err := store.load(process)
		if err != nil {
			t.Fatal(err)
		}
		base := string(Default())
		if process {
			base += processText
		}
		want := []byte(base + "\n\n10\r\n\n\nA\n\n Z\n\n\n user\n")
		if !bytes.Equal(g.Text, want) || g.Digest != Digest(want) {
			t.Fatalf("assembly process=%v: %q", process, g.Text)
		}
		snap, err := store.WriteSnapshot(g.Text)
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(snap.Path)
		if err != nil || !bytes.Equal(content, want) || snap.Digest != g.Digest {
			t.Fatalf("snapshot %v %+v", err, snap)
		}
	}
}

func TestGuidanceSettingsAndLegacyOff(t *testing.T) {
	for _, user := range []string{"", " \n\t"} {
		t.Run(fmt.Sprintf("blank-%d", len(user)), func(t *testing.T) {
			store := newTestStore(t)
			putFragment(t, store, "app.md", "APP")
			if err := store.Save([]byte(user)); err != nil {
				t.Fatal(err)
			}
			if g, err := store.LoadProcess(); err != nil || !g.Off() {
				t.Fatalf("legacy off: %+v %v", g, err)
			}
			if err := store.SaveSettings(Settings{}); err != nil {
				t.Fatal(err)
			}
			if g, err := store.Load(); err != nil || string(g.Text) != string(Default())+"\n\nAPP" {
				t.Fatalf("explicit enabled: %+v %v", g, err)
			}
		})
	}
	store := newTestStore(t)
	putFragment(t, store, "a.md", "A")
	putFragment(t, store, "b.md", "B")
	if err := store.Save([]byte("USER")); err != nil {
		t.Fatal(err)
	}
	settings := Settings{DisabledFragments: []string{"a.md", "missing.md"}}
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if g, err := store.Load(); err != nil || string(g.Text) != string(Default())+"\n\nB\n\nUSER" {
		t.Fatalf("fragment off: %+v %v", g, err)
	}
	if err := store.Disable(); err != nil {
		t.Fatal(err)
	}
	if g, err := store.LoadProcess(); err != nil || !g.Off() || g.Digest != "" {
		t.Fatalf("whole off: %+v %v", g, err)
	}
	if user, err := os.ReadFile(store.Path()); err != nil || string(user) != "USER" {
		t.Fatalf("Disable changed user: %q %v", user, err)
	}
	if err := store.Reset(); err != nil {
		t.Fatal(err)
	}
	if g, err := store.Load(); err != nil || string(g.Text) != string(Default())+"\n\nB" {
		t.Fatalf("reset: %+v %v", g, err)
	}
}

func TestFragmentErrorsAndAssemblyLimit(t *testing.T) {
	t.Run("unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses permissions")
		}
		store := newTestStore(t)
		path := putFragment(t, store, "app.md", "secret")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0600) })
		if g, err := store.Load(); err == nil || len(g.Text) != 0 {
			t.Fatalf("unreadable: %+v %v", g, err)
		}
		if err := store.SaveSettings(Settings{DisabledFragments: []string{"app.md"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err != nil {
			t.Fatalf("disabled unreadable: %v", err)
		}
	})
	for _, size := range []int{MaxSize, MaxSize + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store := newTestStore(t)
			putFragment(t, store, "big.md", strings.Repeat("x", size))
			if g, err := store.Load(); err == nil || len(g.Text) != 0 {
				t.Fatalf("oversize: %+v %v", g, err)
			}
		})
	}
	for _, process := range []bool{false, true} {
		store := newTestStore(t)
		base := len(Default())
		if process {
			base += len(processText)
		}
		path := putFragment(t, store, "fit.md", strings.Repeat("x", MaxSize-base-2))
		if g, err := store.load(process); err != nil || len(g.Text) != MaxSize {
			t.Fatalf("boundary: %d %v", len(g.Text), err)
		}
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString("x")
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.load(process); err == nil {
			t.Fatal("accepted assembly over limit")
		}
	}
	store := newTestStore(t)
	putFragment(t, store, "a.md", strings.Repeat("x", MaxSize/2))
	putFragment(t, store, "b.md", strings.Repeat("x", MaxSize/2))
	if _, err := store.Load(); err == nil {
		t.Fatal("multiple parts exceeded total limit")
	}
}

func TestSettingsStrictParsingAndPrivateAtomicSave(t *testing.T) {
	for _, text := range []string{
		`null`, `[]`, `{"unknown":true}`, `{"Disabled":true}`, `{"disabled":null}`, `{"disabled":"yes"}`,
		`{"disabled":true,"disabled":false}`, `{"disabledFragments":null}`, `{"disabledFragments":["../a.md"]}`,
		`{"disabledFragments":["x\\a.md"]}`, `{"disabledFragments":["a.MD"]}`, `{"disabledFragments":[null]}`,
		`{"disabled":false} {}`, `{"disabledFragments":{}}`, `{"disabled":`,
	} {
		t.Run(text, func(t *testing.T) {
			store := newTestStore(t)
			if err := os.MkdirAll(store.configDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.SettingsPath(), []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			if g, err := store.Load(); err == nil || len(g.Text) != 0 {
				t.Fatalf("accepted invalid policy %s: %+v", text, g)
			}
		})
	}
	store := newTestStore(t)
	settings := Settings{DisabledFragments: []string{"10-app.md", "z.md"}}
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	got, explicit, err := store.LoadSettings()
	if err != nil || !explicit || !reflect.DeepEqual(got, settings) {
		t.Fatalf("roundtrip: %+v %v %v", got, explicit, err)
	}
	if err := os.Chmod(store.SettingsPath(), 0644); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(store.SettingsPath()); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, err)
	}
	before, err := os.ReadFile(store.SettingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSettings(Settings{DisabledFragments: []string{"../bad.md"}}); err == nil {
		t.Fatal("invalid name saved")
	}
	after, err := os.ReadFile(store.SettingsPath())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("invalid save changed policy")
	}
	entries, err := os.ReadDir(store.configDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary residue: %+v %v", entries, err)
	}
}

func TestExplicitSettingsWithMissingOrBlankUserAndWhitespaceFragment(t *testing.T) {
	for _, policy := range []string{`{}`, `{"disabled":false}`, `{"disabledFragments":[]}`} {
		store := newTestStore(t)
		putFragment(t, store, "app.md", " \n")
		if err := os.WriteFile(store.SettingsPath(), []byte(policy), 0600); err != nil {
			t.Fatal(err)
		}
		want := append(Default(), []byte("\n\n \n")...)
		for _, userExists := range []bool{false, true} {
			if userExists {
				if err := store.Save([]byte(" \n")); err != nil {
					t.Fatal(err)
				}
			}
			if g, err := store.Load(); err != nil || !bytes.Equal(g.Text, want) {
				t.Fatalf("policy %s user=%v: %+v %v", policy, userExists, g, err)
			}
		}
	}
}

func TestResetFailureKeepsWholeGuidanceDisabled(t *testing.T) {
	store := newTestStore(t)
	if err := store.SaveSettings(Settings{Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(store.Path(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Reset(); err == nil {
		t.Fatal("Reset accepted a directory at the user path")
	}
	settings, _, err := store.LoadSettings()
	if err != nil || !settings.Disabled {
		t.Fatalf("failed Reset enabled guidance: %+v %v", settings, err)
	}
}
