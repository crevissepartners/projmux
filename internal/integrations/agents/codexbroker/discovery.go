package codexbroker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// discoveryDirName is the single subdirectory one state domain gives the
	// broker. Keeping every artifact in one owner-private directory is what
	// lets a permission check cover the socket, the record, and the startup
	// lock at once.
	discoveryDirName = "broker"
	// discoveryPrefix keeps the artifacts of this component recognizable
	// inside that directory without encoding anything about the endpoint.
	discoveryPrefix = "cb-"
	// discoveryKeyBytes is the digest width of the endpoint/domain key. The
	// directory already scopes the domain, so the key only has to separate
	// endpoints inside it.
	discoveryKeyBytes = 6
	// maxSocketPathBytes is the platform-safe bound for a Unix socket path.
	// It is checked when the Discovery is built rather than at bind time, so
	// an unusable state domain is refused before anything is created.
	maxSocketPathBytes = 100
	// maxRecordBytes bounds the discovery record read.
	maxRecordBytes = 8 << 10

	discoveryDirMode  os.FileMode = 0o700
	discoveryFileMode os.FileMode = 0o600
	// reclaimDialTimeout bounds the liveness probe that decides whether an
	// artifact is stale.
	reclaimDialTimeout = 250 * time.Millisecond
)

// Discovery is the pure location contract of one broker runtime.
//
// A runtime is a singleton per (state domain, endpoint) pair, and that pair is
// the whole of its identity: not a pid, not a wall clock, not a working
// directory, and not whichever runtime happened to answer first. The domain is
// an absolute, owner-private directory supplied by the caller, because this
// package may not reach the process configuration that resolves one.
type Discovery struct {
	domain   string
	endpoint EndpointKey
	key      string
}

// NewDiscovery derives the discovery contract for one state domain and
// endpoint. It creates nothing; every path it names is validated for
// ownership at the moment it is used.
func NewDiscovery(stateDomain string, endpoint EndpointKey) (Discovery, error) {
	domain := strings.TrimSpace(stateDomain)
	if domain == "" || !filepath.IsAbs(domain) {
		return Discovery{}, refuse(RefusalDomainRequired, nil)
	}
	if endpoint == "" {
		endpoint = DefaultEndpointKey
	}
	if !validEndpointKey(endpoint) {
		return Discovery{}, refuse(RefusalEndpointUnknown, nil)
	}
	domain = filepath.Clean(domain)
	sum := sha256.Sum256([]byte(domain + "\x00" + string(endpoint)))
	discovery := Discovery{domain: domain, endpoint: endpoint, key: hex.EncodeToString(sum[:discoveryKeyBytes])}
	if len(discovery.SocketPath()) > maxSocketPathBytes {
		return Discovery{}, refuse(RefusalSocketPathTooLong, nil)
	}
	return discovery, nil
}

// Endpoint returns the endpoint this discovery scopes.
func (d Discovery) Endpoint() EndpointKey { return d.endpoint }

// Domain returns the absolute state domain this discovery is scoped to. It is
// the value a launcher passes to the runtime process it starts, so both sides
// resolve the identical singleton.
func (d Discovery) Domain() string { return d.domain }

// Dir is the owner-private directory holding every artifact of this runtime.
func (d Discovery) Dir() string { return filepath.Join(d.domain, discoveryDirName) }

// SocketPath is the runtime's local IPC endpoint.
func (d Discovery) SocketPath() string {
	return filepath.Join(d.Dir(), discoveryPrefix+d.key+".sock")
}

// RecordPath is the runtime's discovery record.
func (d Discovery) RecordPath() string {
	return filepath.Join(d.Dir(), discoveryPrefix+d.key+".json")
}

// lockPath is the startup mutex that serializes reclaim and launch. It is
// separate from the socket so the mutex survives the artifact it protects.
func (d Discovery) lockPath() string {
	return filepath.Join(d.Dir(), discoveryPrefix+d.key+".lock")
}

// discoveryRecord is the content-free announcement one live runtime publishes.
//
// PID is written for local diagnostics only and is never read as authority:
// a pid is reusable, so deriving ownership from one is exactly the durable
// attribution this package refuses to invent. Liveness is proven by dialing
// the socket, and ownership by the filesystem.
type discoveryRecord struct {
	Protocol    int         `json:"protocol"`
	MinProtocol int         `json:"minProtocol"`
	Endpoint    EndpointKey `json:"endpoint"`
	Runtime     string      `json:"runtime"`
	PID         int         `json:"pid"`
	Credential  string      `json:"credential"`
}

// prepareDiscoveryDir ensures the artifact directory exists and is an
// owner-private real directory. A directory that is a symlink, that is owned
// by another user, or that is group- or world-accessible is refused rather
// than repaired, because repairing it would be indistinguishable from taking
// it over.
func prepareDiscoveryDir(discovery Discovery) error {
	dir := discovery.Dir()
	if err := os.MkdirAll(dir, discoveryDirMode); err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ownedByCurrentUser(info) {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	// #nosec G302 -- 0700 is the intentional owner-private mode for the broker artifact directory.
	if err := os.Chmod(dir, discoveryDirMode); err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	return nil
}

// readRecord reads the discovery record after proving it is an owner-private
// regular file. A record another user could write is a record that could point
// a client at a foreign socket, so it is refused instead of parsed.
func readRecord(discovery Discovery) (discoveryRecord, error) {
	path := discovery.RecordPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return discoveryRecord{}, refuse(RefusalHostUnavailable, err)
	}
	if err != nil {
		return discoveryRecord{}, refuse(RefusalDiscoveryUntrusted, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(info) {
		return discoveryRecord{}, refuse(RefusalDiscoveryUntrusted, nil)
	}
	file, err := os.Open(path) // #nosec G304 -- path is derived from the validated owner-private discovery contract.
	if err != nil {
		return discoveryRecord{}, refuse(RefusalDiscoveryUntrusted, err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil || len(payload) > maxRecordBytes {
		return discoveryRecord{}, refuse(RefusalDiscoveryUntrusted, err)
	}
	var record discoveryRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return discoveryRecord{}, refuse(RefusalDiscoveryUntrusted, err)
	}
	if record.Endpoint != discovery.endpoint || strings.TrimSpace(record.Runtime) == "" ||
		strings.TrimSpace(record.Credential) == "" {
		return discoveryRecord{}, refuse(RefusalDiscoveryUntrusted, nil)
	}
	return record, nil
}

// writeRecord publishes one runtime's record atomically at owner-only mode.
func writeRecord(discovery Discovery, record discoveryRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	temp, err := os.CreateTemp(discovery.Dir(), discoveryPrefix+"record-*.tmp")
	if err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	name := temp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temp.Chmod(discoveryFileMode); err != nil {
		_ = temp.Close()
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	if _, err := temp.Write(payload); err != nil {
		_ = temp.Close()
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	if err := temp.Close(); err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	if err := os.Rename(name, discovery.RecordPath()); err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	return nil
}

// reclaimStale removes the artifacts of a runtime that is provably gone.
//
// "Provably gone" is three facts and nothing else: the socket is a real socket
// this user owns, dialing it is refused, and it is still the same inode when
// the removal happens. A live runtime, a foreign-owned artifact, and anything
// that is not a socket are all left exactly as they are, because every one of
// them may belong to a process this one has no authority over.
func reclaimStale(discovery Discovery) error {
	path := discovery.SocketPath()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		removeRecordIfOwned(discovery)
		return nil
	}
	if err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 || !ownedByCurrentUser(info) {
		return refuse(RefusalDiscoveryUntrusted, nil)
	}
	if conn, dialErr := net.DialTimeout("unix", path, reclaimDialTimeout); dialErr == nil {
		_ = conn.Close()
		return refuse(RefusalHostLive, nil)
	}
	latest, latestErr := os.Lstat(path)
	if latestErr != nil || !os.SameFile(info, latest) ||
		latest.Mode()&os.ModeSocket == 0 || !ownedByCurrentUser(latest) {
		return refuse(RefusalDiscoveryUntrusted, latestErr)
	}
	if err := os.Remove(path); err != nil {
		return refuse(RefusalDiscoveryUntrusted, err)
	}
	removeRecordIfOwned(discovery)
	return nil
}

// removeRecordIfOwned drops a record this user owns. A record without a socket
// cannot be dialed, so leaving it would only offer a credential for an
// endpoint that no longer exists.
func removeRecordIfOwned(discovery Discovery) {
	path := discovery.RecordPath()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) {
		return
	}
	_ = os.Remove(path)
}

// removeIfSame removes path only when it is still the exact object info named.
// It is how a runtime cleans up after itself without deleting the artifacts of
// the runtime that replaced it.
func removeIfSame(path string, info os.FileInfo) {
	if info == nil {
		return
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, current) {
		return
	}
	_ = os.Remove(path)
}
