package localipc

import (
	"errors"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

var errProcessAncestryPID = errors.New("process ancestry pid invalid")

// ProcessAncestryMaxSteps bounds the parent-chain walk of ProcessAncestry.
const ProcessAncestryMaxSteps = 64

// ProcessAncestry reads the identity of pid and of each ancestor, nearest first,
// for at most ProcessAncestryMaxSteps processes. The walk ends at a parent of 1
// or lower, or at a process that is its own parent. A failed step returns the
// identities read so far together with that step's error (a pid of 0 or less
// fails the first step: an empty chain); the partial chain is
// not evidence of who acted.
func ProcessAncestry(pid int) ([]coremetadata.ProcessIdentity, error) {
	if pid <= 0 {
		return []coremetadata.ProcessIdentity{}, errProcessAncestryPID
	}
	return processAncestry(pid, Process)
}

func processAncestry(pid int, read func(int) (coremetadata.ProcessIdentity, int, error)) ([]coremetadata.ProcessIdentity, error) {
	chain := make([]coremetadata.ProcessIdentity, 0, 8)
	for range ProcessAncestryMaxSteps {
		identity, parent, err := read(pid)
		if err != nil {
			return chain, err
		}
		chain = append(chain, identity)
		if parent <= 1 || parent == pid {
			break
		}
		pid = parent
	}
	return chain, nil
}
