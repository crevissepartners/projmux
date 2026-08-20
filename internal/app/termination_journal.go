package app

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// terminationJournal is the lock-free prewrite boundary between a dying
// supervised process and Registry convergence.
//
// One JSON object is written with one O_APPEND write. The file is never
// truncated here: duplicate and stale delivery are already explicit no-ops in
// RecordTermination, and retaining the append history avoids a read/truncate
// race with a supervisor exiting while convergence is consuming earlier rows.
// Retention or GC is deliberately outside this lifecycle slice.
type terminationJournal struct {
	path string
}

const terminationJournalFile = "termination-receipts.jsonl"

func newTerminationJournal(homeDir func() (string, error), lookupEnv func(string) string) (terminationJournal, error) {
	if homeDir == nil && lookupEnv == nil {
		paths, err := config.DefaultPathsFromEnv()
		if err != nil {
			return terminationJournal{}, fmt.Errorf("resolve projmux state paths: %w", err)
		}
		return terminationJournal{path: filepath.Join(paths.StateDir, terminationJournalFile)}, nil
	}
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	home, err := homeDir()
	if err != nil {
		return terminationJournal{}, fmt.Errorf("resolve user home: %w", err)
	}
	paths, err := config.Homes{
		HomeDir:    home,
		ConfigHome: lookupEnv("XDG_CONFIG_HOME"),
		StateHome:  lookupEnv("XDG_STATE_HOME"),
	}.Paths()
	if err != nil {
		return terminationJournal{}, fmt.Errorf("resolve projmux state paths: %w", err)
	}
	return terminationJournal{path: filepath.Join(paths.StateDir, terminationJournalFile)}, nil
}

func (j terminationJournal) append(receipt coremetadata.TerminationEvidence) error {
	if strings.TrimSpace(j.path) == "" {
		return errors.New("termination journal has no path")
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("marshal termination receipt: %w", err)
	}
	// The leading delimiter repairs a previous process's truly unterminated
	// partial tail before placing this complete row. Both delimiters and the JSON
	// body are emitted by one O_APPEND write, so Registry locking is never part
	// of receipt durability.
	framed := make([]byte, 0, len(body)+2)
	framed = append(framed, '\n')
	framed = append(framed, body...)
	framed = append(framed, '\n')
	if err := localstate.EnsurePrivateDir(filepath.Dir(j.path)); err != nil {
		return fmt.Errorf("create termination journal dir: %w", err)
	}
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, localstate.PrivateFileMode)
	if err != nil {
		return fmt.Errorf("open termination journal: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(framed); err != nil {
		return fmt.Errorf("append termination journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync termination journal: %w", err)
	}
	return nil
}

func (j terminationJournal) read() ([]coremetadata.TerminationEvidence, error) {
	if strings.TrimSpace(j.path) == "" {
		return nil, nil
	}
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.Open(j.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open termination journal: %w", err)
	}
	defer file.Close()
	var receipts []coremetadata.TerminationEvidence
	scanner := bufio.NewScanner(file)
	// A receipt is intentionally tiny; bounding a row keeps a corrupt journal
	// from becoming an allocation surface while still leaving later valid rows
	// readable.
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var receipt coremetadata.TerminationEvidence
		if err := json.Unmarshal(line, &receipt); err != nil {
			// The only writer uses one small O_APPEND write, but process or disk
			// failure can still leave a partial tail. A malformed row is not
			// allowed to permanently disable convergence; valid rows before and
			// after it remain independently guarded by Pane uid + generation.
			continue
		}
		receipts = append(receipts, receipt)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read termination journal: %w", err)
	}
	return receipts, nil
}

func absorbTerminationReceipts(working *coremetadata.Registry, mutator coremetadata.Mutator, receipts []coremetadata.TerminationEvidence) (bool, error) {
	changed := false
	for _, receipt := range receipts {
		// The journal is append-only in this phase, so one syntactically valid but
		// semantically impossible row must not poison every future convergence.
		// Apply against a clone: an invalid row is discarded without exposing any
		// partial mutation, while later valid rows still see earlier valid ones.
		candidate := working.Clone()
		outcome, err := mutator.RecordTermination(&candidate, receipt)
		if err != nil {
			continue
		}
		*working = candidate
		changed = changed || outcome.Applied
	}
	return changed, nil
}

func terminationReceiptsNeedAbsorption(registry coremetadata.Registry, mutator coremetadata.Mutator, receipts []coremetadata.TerminationEvidence) bool {
	candidate := registry.Clone()
	changed, _ := absorbTerminationReceipts(&candidate, mutator, receipts)
	return changed
}
