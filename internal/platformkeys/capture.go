package platformkeys

import (
	"context"
	"fmt"
)

// CaptureModifiedChord captures one safe portable physical chord. Non-Darwin
// builds return captured=false immediately so their existing TTY capture path
// remains unchanged.
func CaptureModifiedChord(ctx context.Context) (chord string, captured bool, err error) {
	if !Available() {
		return "", false, nil
	}
	source := NewSource()
	if err := source.Replace(captureBindings()); err != nil {
		return "", false, err
	}
	source.SetEnabled(true)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		runErr <- source.Run(runCtx)
	}()

	select {
	case <-source.Ready():
	case err := <-runErr:
		return "", false, err
	case <-ctx.Done():
		return "", false, nil
	}

	select {
	case chord := <-source.Events():
		cancel()
		if err := <-runErr; err != nil {
			return "", false, err
		}
		return chord, true, nil
	case err := <-runErr:
		return "", false, err
	case <-ctx.Done():
		cancel()
		<-runErr
		return "", false, nil
	}
}

func captureBindings() []Binding {
	keys := make([]string, 0, 80)
	for key := 'a'; key <= 'z'; key++ {
		keys = append(keys, string(key))
	}
	for key := '0'; key <= '9'; key++ {
		keys = append(keys, string(key))
	}
	keys = append(keys,
		"Left", "Right", "Up", "Down",
		"Home", "End", "PageUp", "PageDown", "Delete",
		"Enter", "Tab", "Space", "Backspace", "Escape",
	)
	for number := 1; number <= 20; number++ {
		keys = append(keys, fmt.Sprintf("F%d", number))
	}

	var chords []string
	for _, prefix := range []string{"M-", "C-", "C-M-"} {
		for _, key := range keys {
			chords = append(chords, prefix+key, prefix+"S-"+key)
		}
	}
	return ParseBindings(chords)
}
