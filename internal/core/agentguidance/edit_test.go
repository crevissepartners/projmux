package agentguidance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func editStore(t *testing.T) Store {
	t.Helper()
	dir := t.TempDir()
	return NewStore(filepath.Join(dir, "config"), filepath.Join(dir, "state"))
}
func requireEditCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *EditError
	if !errors.As(err, &e) || e.Code != "agent-guidance-"+code {
		t.Fatalf("want %s: %v", code, err)
	}
}
func guidanceTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !e.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestGuidanceEditingReadValidateNoWrites(t *testing.T) {
	ctx := context.Background()
	s := editStore(t)
	for _, user := range []string{"ABSENT", "", " \n", "custom\r\n"} {
		t.Run(user, func(t *testing.T) {
			if user != "ABSENT" {
				if err := s.Save([]byte(user)); err != nil {
					t.Fatal(err)
				}
			}
			before := guidanceTree(t, filepath.Dir(s.configDir))
			v, err := s.ReadFile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if v.Present != (user != "ABSENT") || v.Bytes != len(v.Text) || v.DefaultText != string(Default()) || v.DefaultDigest != Digest(Default()) || v.MaxBytes != 65536 || v.Revision == "" {
				t.Fatalf("bad view %+v", v)
			}
			candidate, err := s.ValidateFile(ctx, []byte("candidate"))
			if err != nil || candidate.Revision != v.Revision || candidate.ProcessAssembledBytes <= candidate.AssembledBytes {
				t.Fatalf("validate %+v %v", candidate, err)
			}
			if got := guidanceTree(t, filepath.Dir(s.configDir)); !reflect.DeepEqual(got, before) {
				t.Fatalf("read/validate wrote files: %v -> %v", before, got)
			}
			if user == "ABSENT" {
				if _, err := os.Stat(s.configDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("created config directory: %v", err)
				}
			}
		})
	}
}
func TestGuidanceEditingReadEncodingAndRecovery(t *testing.T) {
	ctx := context.Background()
	for _, original := range [][]byte{{0xff, 'x'}, []byte("a\x00b\r\n")} {
		t.Run(Digest(original), func(t *testing.T) {
			s := editStore(t)
			if err := s.Save(original); err != nil {
				t.Fatal(err)
			}
			before := guidanceTree(t, filepath.Dir(s.configDir))
			file, readErr := s.ReadFile(ctx)
			if original[0] == 0xff {
				requireEditCode(t, readErr, "unreadable")
			} else {
				if readErr != nil {
					t.Fatal(readErr)
				}
				encoded, err := json.Marshal(file)
				if err != nil {
					t.Fatal(err)
				}
				var decoded File
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal([]byte(decoded.Text), original) || decoded.Bytes != len(original) || decoded.Digest != Digest(original) {
					t.Fatalf("lossy read JSON: %s", encoded)
				}
				_, err = s.ValidateFile(ctx, original)
				requireEditCode(t, err, "invalid")
			}
			replacement := []byte("repaired\r\n")
			candidate, err := s.ValidateFile(ctx, replacement)
			if err != nil {
				t.Fatal(err)
			}
			if got := guidanceTree(t, filepath.Dir(s.configDir)); !reflect.DeepEqual(got, before) {
				t.Fatalf("read/validate changed existing bytes: %v -> %v", before, got)
			}
			if _, err := s.WriteFile(ctx, replacement, candidate.Revision); err != nil {
				t.Fatal(err)
			}
			file, err = s.ReadFile(ctx)
			if err != nil || file.Text != string(replacement) || file.Bytes != len(replacement) || file.Digest != Digest(replacement) {
				t.Fatalf("recovery read: %+v, %v", file, err)
			}
		})
	}
}
func TestGuidanceEditingLimitsAndFormat(t *testing.T) {
	ctx := context.Background()
	s := editStore(t)
	// Raw 64KiB is accepted when whole guidance is disabled; 64KiB+1 is never accepted.
	if err := s.SetEnabled("", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateFile(ctx, bytes.Repeat([]byte("x"), MaxSize)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		b    []byte
		code string
	}{{"raw overflow", bytes.Repeat([]byte("x"), MaxSize+1), "too-large"}, {"invalid UTF8", []byte{0xff}, "invalid"}, {"NUL", []byte("a\x00b"), "invalid"}} {
		t.Run(tc.name, func(t *testing.T) { _, err := s.ValidateFile(ctx, tc.b); requireEditCode(t, err, tc.code) })
	}
	if err := s.SetEnabled("", true); err != nil {
		t.Fatal(err)
	}
	maxProcess := MaxSize - len(Default()) - len(processText) - 2
	at, err := s.ValidateFile(ctx, bytes.Repeat([]byte("x"), maxProcess))
	if err != nil || at.ProcessAssembledBytes != MaxSize {
		t.Fatalf("process boundary %+v %v", at, err)
	}
	for _, n := range []int{maxProcess + 1, MaxSize - len(Default()) - 2, MaxSize - len(Default()) - 1} {
		_, err := s.ValidateFile(ctx, bytes.Repeat([]byte("x"), n))
		requireEditCode(t, err, "too-large")
	}
	// tmux's exact boundary is also checked independently by the shared assembler.
	for _, n := range []int{MaxSize - len(Default()) - 2, MaxSize - len(Default()) - 1} {
		g, err := assembleGuidance(Settings{}, true, bytes.Repeat([]byte("x"), n), true, false, func(func([]byte) error) error { return nil })
		if n == MaxSize-len(Default())-2 {
			if err != nil || len(g.Text) != MaxSize {
				t.Fatal("tmux boundary", err)
			}
		} else if err == nil {
			t.Fatal("accepted tmux overflow")
		}
	}
	for _, explicit := range []bool{false, true} {
		v := editState{Explicit: explicit}
		got, err := validateCandidate(v, []byte(" \n"))
		if err != nil {
			t.Fatal(err)
		}
		if (got.AssembledBytes == 0) == explicit {
			t.Fatal("legacy whitespace policy changed")
		}
	}
}
func TestGuidanceEditingStaleAndPreservation(t *testing.T) {
	ctx := context.Background()
	for _, change := range []string{"user", "policy", "fragment"} {
		t.Run(change, func(t *testing.T) {
			s := editStore(t)
			if err := s.Save([]byte("old")); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(s.configDir, FragmentsDirName)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			fragment := filepath.Join(dir, "a.md")
			if err := os.WriteFile(fragment, []byte("a"), 0600); err != nil {
				t.Fatal(err)
			}
			v, err := s.ValidateFile(ctx, []byte("new"))
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "user":
				err = s.Save([]byte("external"))
			case "policy":
				err = s.SetEnabled("", false)
			case "fragment":
				err = os.WriteFile(fragment, []byte("changed"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := guidanceTree(t, s.configDir)
			_, err = s.WriteFile(ctx, []byte("new"), v.Revision)
			requireEditCode(t, err, "stale")
			_, err = s.ResetFile(ctx, v.Revision)
			requireEditCode(t, err, "stale")
			if !reflect.DeepEqual(before, guidanceTree(t, s.configDir)) {
				t.Fatal("stale write changed inputs")
			}
			now, err := s.ReadFile(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.WriteFile(ctx, []byte{0}, now.Revision)
			requireEditCode(t, err, "invalid")
			if !reflect.DeepEqual(before, guidanceTree(t, s.configDir)) {
				t.Fatal("invalid write changed inputs")
			}
		})
	}
}
func TestGuidanceEditingMaskedFragmentsAndReset(t *testing.T) {
	s := editStore(t)
	ctx := context.Background()
	if err := s.Save([]byte("old")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.configDir, FragmentsDirName)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "masked.md"), bytes.Repeat([]byte("x"), MaxSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled("masked.md", false); err != nil {
		t.Fatal(err)
	}
	v, err := s.ValidateFile(ctx, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.WriteFile(ctx, []byte("new"), v.Revision)
	if err != nil || out.Text != "new" {
		t.Fatalf("write %+v %v", out, err)
	}
	current, err := s.loadCurrent(false)
	if err != nil || !strings.HasSuffix(string(current.Text), "\n\nnew") {
		t.Fatalf("assembly %v", err)
	}
	if err := s.SetEnabled("", false); err != nil {
		t.Fatal(err)
	}
	out, err = s.ReadFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reset, err := s.ResetFile(ctx, out.Revision)
	if err != nil || reset.Present {
		t.Fatalf("reset %+v %v", reset, err)
	}
	settings, explicit, err := s.LoadSettings()
	if err != nil || !explicit || settings.Disabled || !reflect.DeepEqual(settings.DisabledFragments, []string{"masked.md"}) {
		t.Fatalf("policy %+v %v", settings, err)
	}
}
func TestGuidanceEditingConcurrentRevision(t *testing.T) {
	s := editStore(t)
	ctx := context.Background()
	v, err := s.ReadFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, body := range []string{"one", "two"} {
		wg.Go(func() { _, err := s.WriteFile(ctx, []byte(body), v.Revision); errs <- err })
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			requireEditCode(t, err, "stale")
		}
	}
	if success != 1 {
		t.Fatalf("successful writers=%d", success)
	}
}
func TestGuidanceEditingCancellationAndUnreadable(t *testing.T) {
	s := editStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadFile(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.WriteFile(ctx, []byte("x"), "rev"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.ResetFile(ctx, "rev"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.ValidateFile(ctx, []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.Path(), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReadFile(context.Background())
	requireEditCode(t, err, "unreadable")
}
func TestGuidanceAtomicRenameFailurePreservesDestination(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "previous")
	if err := os.WriteFile(marker, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	before := guidanceTree(t, dir)
	if err := writeAtomic(target, []byte("replacement"), 0600); err == nil {
		t.Fatal("expected rename failure")
	}
	if !reflect.DeepEqual(before, guidanceTree(t, dir)) {
		t.Fatal("failed atomic replacement changed destination or left temp files")
	}
}

func TestGuidanceEditingEmptyWriteRevisionRoundTrip(t *testing.T) {
	ctx := context.Background()
	for _, candidate := range [][]byte{nil, {}, []byte(" \n")} {
		s := editStore(t)
		before, err := s.ReadFile(ctx)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := s.WriteFile(ctx, candidate, before.Revision)
		if err != nil {
			t.Fatal(err)
		}
		read, err := s.ReadFile(ctx)
		if err != nil || saved.Revision != read.Revision || !read.Present {
			t.Fatalf("empty round trip %+v %+v %v", saved, read, err)
		}
		if _, err := s.ResetFile(ctx, saved.Revision); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuidanceEditingAtomicSaveFailurePreservesInputs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary filesystem permission enforcement")
	}
	ctx := context.Background()
	s := editStore(t)
	if err := s.Save([]byte("preserved")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled("", true); err != nil {
		t.Fatal(err)
	}
	// A symlinked config directory is supported. Its existing lock remains
	// writable, while the actual directory refuses temporary-file creation.
	actual := s.configDir
	link := filepath.Join(filepath.Dir(actual), "linked-config")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	s.configDir = link
	v, err := s.ReadFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := guidanceTree(t, actual)
	if err := os.Chmod(actual, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(actual, 0700)
	_, err = s.WriteFile(ctx, []byte("replacement"), v.Revision)
	requireEditCode(t, err, "unreadable")
	if !reflect.DeepEqual(before, guidanceTree(t, actual)) {
		t.Fatal("failed save changed user/policy/fragment/receipt bytes or left temporary files")
	}
}

func TestGuidanceEditingCancellationWhileWaitingForLock(t *testing.T) {
	s := editStore(t)
	background := context.Background()
	before, err := s.ReadFile(background)
	if err != nil {
		t.Fatal(err)
	}
	err = s.withPolicyLock(func() error {
		ctx, cancel := context.WithTimeout(background, 20*time.Millisecond)
		defer cancel()
		_, err := s.WriteFile(ctx, []byte("unadmitted"), before.Revision)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("write cancellation lost: %v", err)
		}
		ctx, cancel = context.WithTimeout(background, 20*time.Millisecond)
		defer cancel()
		_, err = s.ResetFile(ctx, before.Revision)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("reset cancellation lost: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.ReadFile(background)
	if err != nil || after.Present || after.Revision != before.Revision {
		t.Fatalf("canceled waiter wrote guidance: %+v %v", after, err)
	}
}
