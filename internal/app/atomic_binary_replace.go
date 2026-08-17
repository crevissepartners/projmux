package app

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// atomicBinaryReplacer is update plumbing, not a CLI command. It stages a
// downloaded release beside the active executable and atomically swaps it in.
type atomicBinaryReplacer struct {
	rename        func(oldpath, newpath string) error
	chmod         func(name string, mode os.FileMode) error
	remove        func(name string) error
	copyFile      func(src, dst string) error
	tempSuffixGen func() string
}

func (c *atomicBinaryReplacer) replace(src, target string) error {
	suffix := "tmp"
	if c.tempSuffixGen != nil {
		suffix = c.tempSuffixGen()
	}
	tmpfile := target + ".upgrade." + suffix
	if c.rename == nil {
		return errors.New("configure update rename: rename function is not configured")
	}
	if err := c.rename(src, tmpfile); err != nil {
		// Cross-device or other rename failure: fall back to copy + remove.
		if c.copyFile == nil {
			return fmt.Errorf("rename installed binary into target directory: %w", err)
		}
		if copyErr := c.copyFile(src, tmpfile); copyErr != nil {
			return fmt.Errorf("rename and copy installed binary into target directory failed: rename=%v copy=%w", err, copyErr)
		}
		if c.remove != nil {
			_ = c.remove(src)
		}
	}
	if c.chmod != nil {
		if err := c.chmod(tmpfile, 0o755); err != nil {
			if c.remove != nil {
				_ = c.remove(tmpfile)
			}
			return fmt.Errorf("chmod update staging file: %w", err)
		}
	}
	if err := c.rename(tmpfile, target); err != nil {
		if c.remove != nil {
			_ = c.remove(tmpfile)
		}
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("permission denied: %s", target)
		}
		return fmt.Errorf("atomically replace %s: %w", target, err)
	}
	return nil
}

func copyRegularFile(src, dst string) error {
	// #nosec G304 -- src is the freshly downloaded update binary selected by
	// update plumbing, and dst is its staging sibling beside the active binary.
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source binary for copy: %w", err)
	}
	defer in.Close()
	// #nosec G302,G304 -- the destination is the update staging sibling and
	// must remain executable before the atomic replacement.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("open destination binary for copy: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy binary content: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination after copy: %w", err)
	}
	return nil
}

func defaultTempSuffix() string {
	return fmt.Sprintf("%d", os.Getpid())
}
