package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

// The producer's not-prepared failure keeps its text and error code, and the
// failed reply carries the structured marker; no other failure carries one.
func TestEncodeDetachedResumeErrorCarriesNotPreparedMarker(t *testing.T) {
	encode := func(err error) mutationruntime.ProcessDetachedReply {
		t.Helper()
		var out bytes.Buffer
		_ = encodeDetachedResumeError(json.NewEncoder(&out), coremetadata.ProcessIdentity{}, processhost.Binding{}, err)
		var reply mutationruntime.ProcessDetachedReply
		if e := json.Unmarshal(out.Bytes(), &reply); e != nil {
			t.Fatal(e)
		}
		return reply
	}
	reply := encode(mutationruntime.ErrResumeNotPrepared)
	if reply.Error != "process-resume-not-prepared" || reply.ErrorCode != "resume-execution-failed" {
		t.Fatalf("text %q code %q", reply.Error, reply.ErrorCode)
	}
	if !mutationruntime.IsResumeNotPreparedDetail(reply.ErrorDetail) {
		t.Fatalf("marker missing: %s", reply.ErrorDetail)
	}
	other := encode(errors.New("something failed"))
	if len(other.ErrorDetail) != 0 || other.ErrorCode != "resume-execution-failed" {
		t.Fatalf("other failure: detail %s code %q", other.ErrorDetail, other.ErrorCode)
	}
}
