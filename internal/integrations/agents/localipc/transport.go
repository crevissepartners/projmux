// Package localipc owns the provider-neutral Unix transport discipline shared
// by exact Agent adapters. Protocol identity and operation semantics remain in
// the provider adapters; this package only owns private sockets and bounded
// one-object JSON frames.
package localipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"
)

const (
	MaxFrameBytes = 64 << 10
	MaxSocketPath = 100
	DialTimeout   = 500 * time.Millisecond
	Deadline      = 5 * time.Second
)

var (
	ErrFrameTooLarge  = errors.New("bounded frame is too large")
	ErrFrameMalformed = errors.New("bounded frame is malformed")
	ErrSocketReplaced = errors.New("private endpoint identity changed")
)

// SocketIdentity is the stable kernel identity of one owned Unix socket.
// Paths alone are never sufficient when deciding whether cleanup is safe.
type SocketIdentity struct {
	Device                uint64      `json:"device"`
	Inode                 uint64      `json:"inode"`
	Owner                 uint32      `json:"owner"`
	Mode                  os.FileMode `json:"mode"`
	Size                  int64       `json:"size"`
	ChangeTimeSeconds     int64       `json:"changeTimeSeconds"`
	ChangeTimeNanoseconds int64       `json:"changeTimeNanoseconds"`
}

// InspectOwnedSocket rejects symlinks, non-sockets, foreign owners, and modes
// other than 0600.
func InspectOwnedSocket(path string) (SocketIdentity, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > MaxSocketPath {
		return SocketIdentity{}, errors.New("private endpoint is unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 ||
		info.Mode().Perm() != 0o600 || !OwnedByCurrentUser(info) {
		return SocketIdentity{}, errors.New("private endpoint is unavailable")
	}
	identity, ok := socketIdentity(info)
	if !ok {
		return SocketIdentity{}, errors.New("private endpoint is unavailable")
	}
	return identity, nil
}

// PrepareSocket creates and secures the parent directory, refuses live or
// foreign collisions, and removes only an unchanged owned stale socket.
func PrepareSocket(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > MaxSocketPath {
		return errors.New("private endpoint path exceeds the platform-safe bound")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create private endpoint directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !OwnedByCurrentUser(info) {
		return errors.New("private endpoint directory is not a private owned directory")
	}
	// #nosec G302 -- 0700 is the intentional owner-only directory mode.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure private endpoint directory: %w", err)
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect private endpoint: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || !OwnedByCurrentUser(info) {
		return errors.New("private endpoint collision is not an owned socket")
	}
	before, ok := socketIdentity(info)
	if !ok {
		return errors.New("private endpoint collision identity is unavailable")
	}
	conn, dialErr := net.DialTimeout("unix", path, DialTimeout)
	if dialErr == nil {
		_ = conn.Close()
		return errors.New("private endpoint is already active")
	}
	latest, latestErr := os.Lstat(path)
	after, identityOK := socketIdentity(latest)
	if latestErr != nil || !identityOK || !sameSocketIdentity(before, after) || latest.Mode()&os.ModeSocket == 0 || !OwnedByCurrentUser(latest) {
		return errors.New("private stale endpoint changed during ownership check")
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale private endpoint: %w", err)
	}
	return nil
}

// Listener remembers the created inode so Close never unlinks a replacement.
type Listener struct {
	Unix     *net.UnixListener
	Path     string
	identity SocketIdentity
}

func (l *Listener) Identity() SocketIdentity {
	if l == nil {
		return SocketIdentity{}
	}
	return l.identity
}

func Listen(path string) (*Listener, error) {
	if err := PrepareSocket(path); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen private endpoint: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure private endpoint: %w", err)
	}
	identity, err := InspectOwnedSocket(path)
	if err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return &Listener{Unix: listener, Path: path, identity: identity}, nil
}

func (l *Listener) Close() error {
	if l == nil || l.Unix == nil {
		return nil
	}
	err := l.Unix.Close()
	_ = RemoveOwnedSocket(l.Path, l.identity)
	return err
}

// RemoveOwnedSocket unlinks only the exact socket inode recorded by its owner.
// Crash cleanup may carry this identity in a non-secret lease receipt; a
// replacement which has claimed the same path is never removed.
func RemoveOwnedSocket(path string, expected SocketIdentity) error {
	if expected.Inode == 0 || expected.Mode&os.ModeSocket == 0 || expected.Mode.Perm() != 0o600 {
		return ErrSocketReplaced
	}
	current, err := InspectOwnedSocket(path)
	if err != nil {
		return err
	}
	if !sameSocketIdentity(current, expected) {
		return ErrSocketReplaced
	}
	return os.Remove(path)
}

// ReadJSON reads exactly one complete JSON value bounded by MaxFrameBytes.
func ReadJSON(reader io.Reader, value any) error {
	payload, err := io.ReadAll(io.LimitReader(reader, MaxFrameBytes+1))
	if len(payload) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	if err != nil {
		return errors.New("bounded frame read failed")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(value); err != nil {
		return ErrFrameMalformed
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrFrameMalformed
	}
	return nil
}

// MarshalJSON emits the same newline-terminated single frame as json.Encoder.
func MarshalJSON(value any) ([]byte, error) {
	var payload bytes.Buffer
	if err := json.NewEncoder(&payload).Encode(value); err != nil || payload.Len() > MaxFrameBytes {
		return nil, errors.New("bounded frame encode failed")
	}
	return payload.Bytes(), nil
}

func WriteJSON(writer io.Writer, value any) error {
	payload, err := MarshalJSON(value)
	if err != nil {
		return err
	}
	for len(payload) > 0 {
		n, writeErr := writer.Write(payload)
		if n > 0 {
			payload = payload[n:]
		}
		if writeErr != nil {
			return writeErr
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func OwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func socketIdentity(info os.FileInfo) (SocketIdentity, bool) {
	if info == nil {
		return SocketIdentity{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return SocketIdentity{}, false
	}
	seconds, nanoseconds := statChangeTime(stat)
	return SocketIdentity{
		Device:                uint64(stat.Dev),
		Inode:                 stat.Ino,
		Owner:                 stat.Uid,
		Mode:                  info.Mode(),
		Size:                  info.Size(),
		ChangeTimeSeconds:     seconds,
		ChangeTimeNanoseconds: nanoseconds,
	}, true
}

func sameSocketIdentity(first, second SocketIdentity) bool {
	return first == second
}

// Stat_t spells the change-time field Ctim on Linux and Ctimespec on Darwin.
// Reflection keeps this package inside the repository's explicit two-OS
// contract without build constraints or narrowing conversions.
func statChangeTime(stat *syscall.Stat_t) (int64, int64) {
	value := reflect.ValueOf(stat).Elem()
	for _, name := range []string{"Ctim", "Ctimespec"} {
		field := value.FieldByName(name)
		if !field.IsValid() {
			continue
		}
		seconds, nanoseconds := field.FieldByName("Sec"), field.FieldByName("Nsec")
		if seconds.IsValid() && nanoseconds.IsValid() && seconds.CanInt() && nanoseconds.CanInt() {
			return seconds.Int(), nanoseconds.Int()
		}
	}
	return 0, 0
}
