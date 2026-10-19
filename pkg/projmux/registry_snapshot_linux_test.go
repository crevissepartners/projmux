package projmux

import (
	"context"
	"encoding/binary"
	"errors"
	"syscall"
	"testing"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestReadRegistrySnapshotSingleReadNoFilesystemEffects(t *testing.T) {
	c, _ := snapshotClient(t, snapshotRaw(t))
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	path := store.PathFor(c.paths.StateDir)
	// Opening and closing events alternate, so the kernel cannot coalesce two
	// reads into one. Directory watches also catch transient lock/repair writes.
	if _, err = syscall.InotifyAddWatch(fd, path, syscall.IN_OPEN|syscall.IN_CLOSE_NOWRITE|syscall.IN_MODIFY|syscall.IN_ATTRIB); err != nil {
		t.Fatal(err)
	}
	if _, err = syscall.InotifyAddWatch(fd, c.paths.StateDir+"/metadata", syscall.IN_CREATE|syscall.IN_DELETE|syscall.IN_MOVED_FROM|syscall.IN_MOVED_TO|syscall.IN_MODIFY|syscall.IN_ATTRIB); err != nil {
		t.Fatal(err)
	}
	c.transport = nil
	if _, err = c.ReadRegistrySnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	opens, closes := 0, 0
	for {
		var buf [4096]byte
		n, e := syscall.Read(fd, buf[:])
		if errors.Is(e, syscall.EAGAIN) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			break
		}
		for off := 0; off < n; {
			mask := binary.NativeEndian.Uint32(buf[off+4:])
			size := int(binary.NativeEndian.Uint32(buf[off+12:]))
			off += syscall.SizeofInotifyEvent + size
			switch mask {
			case syscall.IN_OPEN:
				opens++
			case syscall.IN_CLOSE_NOWRITE:
				closes++
			default:
				t.Fatalf("filesystem effect mask %#x", mask)
			}
		}
	}
	if opens != 1 || closes != 1 {
		t.Fatalf("registry read count: open=%d close=%d", opens, closes)
	}
}
