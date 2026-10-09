package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"syscall"
)

// codexBrokerImageBytes is the digest width of one executable image token.
const codexBrokerImageBytes = 8

// codexBrokerImage is the image token a new broker binding is keyed by. It is a
// variable so tests can pin it without depending on the test binary's file.
var codexBrokerImage = currentCodexBrokerImage

// currentCodexBrokerImage names the executable currently installed at this
// process's own path.
//
// It reads the file at the path, not this process's running image: an install
// replaces the file atomically, so a process that started before the install
// -- a long-lived web server, an older shell -- still keys new bindings by the
// image the install published, and every caller that creates an Agent after an
// install meets the same fresh runtime. The token is the file's identity
// (device, inode, size, modification time), which an atomic replace always
// changes and an unrelated read never does. An unreadable executable returns
// the empty legacy token, which keeps the pre-image singleton contract.
func currentCodexBrokerImage() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return codexBrokerImageOf(exe)
}

// codexBrokerImageOf is the image token of the file installed at one
// executable path. A Linux `(deleted)` suffix names the path the running image
// was unlinked from, which is exactly where the installed file now is.
func codexBrokerImageOf(exe string) string {
	path, _ := codexProcessImagePath(exe)
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	material := fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano())
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:codexBrokerImageBytes])
}
