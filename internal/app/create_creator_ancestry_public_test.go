package app

import (
	"os"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestProcessCreatorAncestryEqualsPublicReadProcessAncestry(t *testing.T) {
	chain, err := processCreatorAncestry()
	public, publicErr := projmux.ReadProcessAncestry(os.Getpid())
	if (err == nil) != (publicErr == nil) || len(chain) != len(public) || len(chain) == 0 {
		t.Fatalf("creator %v, %v; public %v, %v", chain, err, public, publicErr)
	}
	want := make([]core.ProcessIdentity, len(chain))
	for i, identity := range chain {
		want[i] = core.ProcessIdentity{PID: identity.PID, OwnerUID: identity.OwnerUID, Start: identity.Start}
	}
	if !reflect.DeepEqual(public, want) {
		t.Fatalf("public %v, creator %v", public, want)
	}
}
