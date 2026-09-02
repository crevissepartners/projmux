package codexbundle

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeBundleSource(t *testing.T, root string) []ArtifactSpec {
	t.Helper()
	files := map[string]string{
		"bin/codex":                "#!/bin/sh\nprintf 'codex-ok\\n'\n",
		"bin/codex-code-mode-host": "#!/bin/sh\nprintf 'host-ok\\n'\n",
		"codex-path/rg":            "#!/bin/sh\nprintf 'rg-ok\\n'\n",
		"codex-resources/bwrap":    "#!/bin/sh\nprintf 'bwrap-ok\\n'\n",
	}
	for path, body := range files {
		target := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return []ArtifactSpec{
		{Path: "bin/codex", Roles: []Role{RoleServer, RoleTUI}},
		{Path: "bin/codex-code-mode-host", Roles: []Role{RoleHelper}},
		{Path: "codex-path/rg", Roles: []Role{RoleHelper}},
		{Path: "codex-resources/bwrap", Roles: []Role{RoleHelper}},
	}
}

func TestContentAddressedBundleSurvivesSourceRemovalForServerTUIAndHelpers(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "upstream-current")
	store := filepath.Join(root, "leases")
	specs := writeBundleSource(t, source)
	manifest, err := Inspect(source, "0.152.0", ProtocolRange{Min: 2, Max: 2}, specs)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := Create(store, source, manifest, ProtocolRange{Min: 2, Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(source); !os.IsNotExist(err) {
		t.Fatalf("upstream source still exists: %v", err)
	}
	reopened, err := Open(lease.Root, ProtocolRange{Min: 2, Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != lease.ID {
		t.Fatalf("reopened lease id=%s want=%s", reopened.ID, lease.ID)
	}
	for _, role := range []Role{RoleServer, RoleTUI, RoleHelper} {
		paths := reopened.Paths(role)
		if len(paths) == 0 {
			t.Fatalf("leased role %s has no executable", role)
		}
		for _, path := range paths {
			output, err := exec.Command(path).CombinedOutput() // #nosec G204 -- exact verified lease path.
			if err != nil || !strings.HasSuffix(strings.TrimSpace(string(output)), "-ok") {
				t.Fatalf("launch %s role=%s output=%q err=%v", filepath.Base(path), role, output, err)
			}
		}
	}
}

func TestBundleDriftAndProtocolMismatchRefuseBeforeFinalCommit(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(t *testing.T, source string, manifest *Manifest)
		required ProtocolRange
		want     Refusal
	}{
		{name: "hash drift", mutate: func(t *testing.T, source string, _ *Manifest) {
			if err := os.WriteFile(filepath.Join(source, "bin/codex"), []byte("#!/bin/sh\nprintf changed\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, required: ProtocolRange{Min: 2, Max: 2}, want: RefusalArtifactSizeDrift},
		{name: "same-size hash drift", mutate: func(t *testing.T, source string, manifest *Manifest) {
			path := filepath.Join(source, "bin/codex")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			raw[len(raw)-2] ^= 1
			if err := os.WriteFile(path, raw, os.FileMode(manifest.Artifacts[0].Mode)); err != nil {
				t.Fatal(err)
			}
		}, required: ProtocolRange{Min: 2, Max: 2}, want: RefusalArtifactHashDrift},
		{name: "mode drift", mutate: func(t *testing.T, source string, _ *Manifest) {
			if err := os.Chmod(filepath.Join(source, "bin/codex"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, required: ProtocolRange{Min: 2, Max: 2}, want: RefusalArtifactModeDrift},
		{name: "protocol mismatch", mutate: func(_ *testing.T, _ string, _ *Manifest) {}, required: ProtocolRange{Min: 3, Max: 3}, want: RefusalProtocolMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source, store := filepath.Join(root, "source"), filepath.Join(root, "store")
			manifest, err := Inspect(source, "0.152.0", ProtocolRange{Min: 2, Max: 2}, writeBundleSource(t, source))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, source, &manifest)
			_, err = Create(store, source, manifest, test.required)
			if got := RefusalOf(err); got != test.want {
				t.Fatalf("refusal=%s want=%s err=%v", got, test.want, err)
			}
			id, idErr := manifest.ContentID()
			if idErr != nil {
				t.Fatal(idErr)
			}
			if _, statErr := os.Lstat(filepath.Join(store, id)); !os.IsNotExist(statErr) {
				t.Fatalf("refused lease committed final directory: %v", statErr)
			}
		})
	}
}

func TestBundleRefusesNonCanonicalRootsAndVersions(t *testing.T) {
	root := t.TempDir()
	source, store := filepath.Join(root, "source"), filepath.Join(root, "store")
	specs := writeBundleSource(t, source)
	for _, padded := range []string{" " + source, source + " ", source + string(os.PathSeparator)} {
		if _, err := Inspect(padded, "0.152.0", ProtocolRange{Min: 2, Max: 2}, specs); RefusalOf(err) != RefusalManifestInvalid {
			t.Fatalf("Inspect root %q refusal=%s err=%v", padded, RefusalOf(err), err)
		}
	}
	if _, err := Inspect(source, " 0.152.0", ProtocolRange{Min: 2, Max: 2}, specs); RefusalOf(err) != RefusalManifestInvalid {
		t.Fatalf("padded version refusal=%s err=%v", RefusalOf(err), err)
	}
	manifest, err := Inspect(source, "0.152.0", ProtocolRange{Min: 2, Max: 2}, specs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(" "+store, source, manifest, ProtocolRange{Min: 2, Max: 2}); RefusalOf(err) != RefusalManifestInvalid {
		t.Fatalf("padded store refusal=%s err=%v", RefusalOf(err), err)
	}
}

func TestCommittedBundleTamperingIsRefusedBeforeLaunch(t *testing.T) {
	root := t.TempDir()
	source, store := filepath.Join(root, "source"), filepath.Join(root, "store")
	manifest, err := Inspect(source, "0.152.0", ProtocolRange{Min: 2, Max: 2}, writeBundleSource(t, source))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := Create(store, source, manifest, ProtocolRange{Min: 2, Max: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(lease.Root, "bin/codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(lease.Root, ProtocolRange{Min: 2, Max: 2}); RefusalOf(err) != RefusalArtifactModeDrift {
		t.Fatalf("tampered lease refusal=%s err=%v", RefusalOf(err), err)
	}
}

func TestCommittedBundleRefusesMatchingSymlinksBeforeLaunch(t *testing.T) {
	for _, relative := range []string{"bin/codex", "manifest.json"} {
		t.Run(relative, func(t *testing.T) {
			root := t.TempDir()
			source, store := filepath.Join(root, "source"), filepath.Join(root, "store")
			manifest, err := Inspect(source, "0.152.0", ProtocolRange{Min: 2, Max: 2}, writeBundleSource(t, source))
			if err != nil {
				t.Fatal(err)
			}
			lease, err := Create(store, source, manifest, ProtocolRange{Min: 2, Max: 2})
			if err != nil {
				t.Fatal(err)
			}
			leasedPath := filepath.Join(lease.Root, filepath.FromSlash(relative))
			outside := filepath.Join(root, "outside-"+filepath.Base(relative))
			if err := os.Rename(leasedPath, outside); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, leasedPath); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(lease.Root, ProtocolRange{Min: 2, Max: 2}); RefusalOf(err) != RefusalLeaseDrift {
				t.Fatalf("matching symlink refusal=%s err=%v", RefusalOf(err), err)
			}
		})
	}
}

func TestManifestCanonicalizationMakesRoleAndArtifactOrderIrrelevant(t *testing.T) {
	root := t.TempDir()
	specs := writeBundleSource(t, root)
	a, err := Inspect(root, "0.152.0", ProtocolRange{Min: 2, Max: 2}, specs)
	if err != nil {
		t.Fatal(err)
	}
	reversed := append([]ArtifactSpec(nil), specs...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	reversed[len(reversed)-1].Roles = []Role{RoleTUI, RoleServer}
	b, err := Inspect(root, "0.152.0", ProtocolRange{Min: 2, Max: 2}, reversed)
	if err != nil {
		t.Fatal(err)
	}
	idA, _ := a.ContentID()
	idB, _ := b.ContentID()
	if idA != idB || !reflect.DeepEqual(a, b) {
		t.Fatalf("canonical manifests differ: %s %s", idA, idB)
	}
}
