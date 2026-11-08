package projmux

import (
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// CodeProcessAncestryUnobservable is the Code of the refusal ReadProcessAncestry
// returns when a step of the walk could not be read.
const CodeProcessAncestryUnobservable Code = "process-ancestry-unobservable"

// ProcessAncestryMaxSteps is the most processes ReadProcessAncestry returns.
const ProcessAncestryMaxSteps = localipc.ProcessAncestryMaxSteps

// ReadProcessAncestry reads the identity of pid and of each of its ancestors,
// nearest first, for at most ProcessAncestryMaxSteps processes. The walk ends at
// a parent of 1 or lower or at a process that is its own parent; a chain longer
// than the bound is cut there without an error. The caller passes its own pid
// (os.Getpid()); the SDK does not read it.
//
// A step that cannot be read returns the identities read so far together with a
// *Refusal carrying CodeProcessAncestryUnobservable, whose message is the cause's
// and whose Unwrap is the cause. A pid of 0 or less fails the first step: an empty
// chain with the same refusal. The chain is a snapshot; ProcessIdentity's start
// time tells a reused pid apart.
//
// A partial chain is not evidence of who acted: a caller must not treat it as
// proof that the process has, or lacks, any ancestor.
func ReadProcessAncestry(pid int) ([]core.ProcessIdentity, error) {
	return publicProcessAncestry(pid, localipc.ProcessAncestry)
}

// publicProcessAncestry converts one ancestry read; read is the seam tests use to
// inject a failed step.
func publicProcessAncestry(pid int, read func(int) ([]metadata.ProcessIdentity, error)) ([]core.ProcessIdentity, error) {
	chain, err := read(pid)
	out := make([]core.ProcessIdentity, len(chain))
	for i := range chain {
		out[i] = publicProcessIdentity(chain[i])
	}
	if err != nil {
		return out, &Refusal{Code: CodeProcessAncestryUnobservable, message: err.Error(), cause: err}
	}
	return out, nil
}
