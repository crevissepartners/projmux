package consumer_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
)

// Run only the support facade in a fresh consumer process. No Client, sampler,
// or settings API is constructed in the helper, including during setup.
func TestExternalResourcesSupportedHelper(t *testing.T) {
	if os.Getenv("SDK_RESOURCE_SUPPORT_HELPER") != "1" {
		return
	}
	before := os.Environ()
	first := projmux.ResourcesSupported()
	for range 100 {
		if projmux.ResourcesSupported() != first {
			t.Fatal("build support changed between calls")
		}
	}
	if !reflect.DeepEqual(before, os.Environ()) {
		t.Fatal("support read changed the process environment")
	}
}

func TestExternalResourcesSupportedEffectFree(t *testing.T) {
	for _, seeded := range []bool{false, true} {
		t.Run(strconv.FormatBool(seeded), func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			config := filepath.Join(root, "config")
			state := filepath.Join(root, "state")
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(root, "commands.log")
			for _, name := range []string{"tmux", "projmux", "claude", "codex", "antigravity"} {
				script := "#!/bin/sh\nprintf '%s\\n' " + strconv.Quote(name) + " >> " + strconv.Quote(log) + "\nexit 99\n"
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if seeded {
				for _, path := range []string{
					filepath.Join(config, "projmux", "config.toml"),
					filepath.Join(state, "projmux", "registry.json"),
					filepath.Join(state, "projmux", "live-resources-sample.json"),
				} {
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("invalid\x00["), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := consumerPathTree(t, root)
			cmd := exec.Command(os.Args[0], "-test.run=^TestExternalResourcesSupportedHelper$", "-test.count=1")
			// A minimal environment prevents inherited runtime/provider routing.
			cmd.Env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + config, "XDG_STATE_HOME=" + state,
				"XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "PATH=" + bin, "SDK_RESOURCE_SUPPORT_HELPER=1"}
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated support consumer: %v\n%s", err, out)
			}
			if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("runtime/CLI/provider command observed: %v", err)
			}
			if !reflect.DeepEqual(before, consumerPathTree(t, root)) {
				t.Fatal("support read changed fixture/cache contents, modes, timestamps, or paths")
			}
		})
	}
}
