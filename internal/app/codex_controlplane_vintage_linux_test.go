package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestProcessImageListerReadsTheLiveTable covers the one part of the vintage
// signal that synthetic images cannot.
//
// Everything above it is a pure projection over a slice, and that projection is
// pinned thoroughly — but a projection over an empty slice reports "no
// control-plane process observed", which is exactly what a correct reader says
// on a machine with none running. The two are indistinguishable from the
// outside, so a lister that silently returned nothing would leave the whole
// signal reporting a reassuring sentence forever.
//
// Reading the current process out of the table is the smallest claim that
// separates them: this process certainly exists.
func TestProcessImageListerReadsTheLiveTable(t *testing.T) {
	images, supported := defaultCodexProcessImages()
	if !supported {
		t.Fatal("the process table was unreadable on a platform that has one")
	}
	if len(images) == 0 {
		t.Fatal("the process table read as empty, which would make every vintage answer a silent no-op")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve own executable: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		resolved = self
	}
	var found *codexProcessImage
	for index, image := range images {
		if image.PID == os.Getpid() {
			found = &images[index]
			break
		}
	}
	if found == nil {
		t.Fatalf("the reader listed %d process(es) and not itself", len(images))
	}
	path, replaced := codexProcessImagePath(found.Exe)
	if path != resolved {
		t.Fatalf("own image = %q (replaced=%v), want the running test binary at %q", path, replaced, resolved)
	}
	if len(found.Cmdline) == 0 {
		t.Fatal("own argv read as empty, which would classify every process into no control-plane role")
	}
}
