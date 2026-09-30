package persona

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRefusalsCallTheFileInstructions pins the text of every refusal and
// store error: the public name is instructions, the reason token keeps its
// persona- spelling, and SpelledAs gives the deprecated `persona` spellings
// their old noun back.
func TestRefusalsCallTheFileInstructions(t *testing.T) {
	t.Parallel()
	store, configDir, _ := newTestStore(t)
	absent := filepath.Join(configDir, DirName, "absent"+FileExt)

	_, err := store.Load("absent")
	want := ReasonNotFound + `: instructions "absent" does not exist at ` + absent
	if err == nil || err.Error() != want {
		t.Fatalf("Load(absent) = %v, want %q", err, want)
	}
	if err := SpelledAs(err, DeprecatedNoun); err.Error() != ReasonNotFound+`: persona "absent" does not exist at `+absent {
		t.Fatalf("SpelledAs(persona) = %q, want the persona noun", err)
	}
	if ReasonOf(err) != ReasonNotFound {
		t.Fatalf("SpelledAs changed the reason: %v", err)
	}

	for name, test := range map[string]struct {
		err  error
		want string
	}{
		"delete missing": {store.Delete("absent"), ReasonNotFound + `: instructions "absent" does not exist at ` + absent},
		"invalid name":   {ValidateName(".hidden"), ReasonNameInvalid + `: instructions ".hidden" `},
		"too large":      {func() error { _, err := store.Write("big", bytes.Repeat([]byte("x"), MaxSize+1)); return err }(), ReasonTooLarge + `: instructions "big" is 65537 bytes`},
		"digest":         {func() error { _, err := store.SnapshotPath("md5:00"); return err }(), `instructions digest "md5:00" is not sha256:<64 lowercase hex>`},
		"unnamed":        {func() error { _, err := store.RecordedSnapshotPath(""); return err }(), ReasonUnavailable + ": has no recorded digest"},
	} {
		if test.err == nil || !strings.HasPrefix(test.err.Error(), test.want) {
			t.Errorf("%s = %v, want it to start %q", name, test.err, test.want)
		}
	}
}

// TestStoreErrorsCallTheFilesInstructions pins the wrapping text of the store
// errors that are not refusals. A regular file where the persona and
// snapshot directories belong makes every read and write fail.
func TestStoreErrorsCallTheFilesInstructions(t *testing.T) {
	t.Parallel()
	store, configDir, stateDir := newTestStore(t)
	for _, dir := range []string{configDir, stateDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, DirName), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, test := range map[string]struct {
		err  error
		want string
	}{
		"load":     {func() error { _, err := store.Load("reviewer"); return err }(), `read instructions "reviewer": `},
		"list":     {func() error { _, err := store.List(); return err }(), "list instructions: "},
		"write":    {func() error { _, err := store.Write("reviewer", []byte("x")); return err }(), `write instructions "reviewer": `},
		"delete":   {store.Delete("reviewer"), `delete instructions "reviewer": `},
		"snapshot": {func() error { _, err := store.WriteSnapshot([]byte("x")); return err }(), "write instructions snapshot: "},
	} {
		if test.err == nil || !strings.HasPrefix(test.err.Error(), test.want) {
			t.Errorf("%s = %v, want it to start %q", name, test.err, test.want)
		}
	}
}
