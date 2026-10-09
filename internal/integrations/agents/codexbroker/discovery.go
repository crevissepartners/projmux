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
	// maxImageBytes bounds the image generation token a discovery carries.
	maxImageBytes = 64
	// maxPublishedRecords bounds how many records one directory scan reads.
	maxPublishedRecords = 64
)

// Discovery is the pure location contract of one broker runtime.
//
// A runtime is a singleton per (state domain, endpoint, image) triple, and that
// triple is the whole of its identity: not a pid, not a wall clock, not a
// working directory, and not whichever runtime happened to answer first. The
// domain is an absolute, owner-private directory supplied by the caller,
// because this package may not reach the process configuration that resolves
// one. The image is an opaque token naming the installed executable a client
// expects to run its runtime; an empty image is the legacy contract every build
// used before images existed, so an older client and an older runtime still
// meet on the same socket.
type Discovery struct {
	domain   string
	endpoint EndpointKey
	image    string
	key      string
}

// NewDiscovery derives the discovery contract for one state domain and
// endpoint. It creates nothing; every path it names is validated for
// ownership at the moment it is used.
func NewDiscovery(stateDomain string, endpoint EndpointKey) (Discovery, error) {
	return NewImageDiscovery(stateDomain, endpoint, "")
}

// NewImageDiscovery derives the discovery contract for one state domain,
// endpoint, and executable image.
//
// Two images of one endpoint are two runtimes. That is what lets a runtime
// started by a newly installed executable serve new bindings at once while the
// runtime of the image it superseded keeps carrying the bindings it already
// holds and drains: neither has to take the other's socket.
func NewImageDiscovery(stateDomain string, endpoint EndpointKey, image string) (Discovery, error) {
	if !validImage(image) {
		return Discovery{}, refuse(RefusalEndpointUnknown, nil)
	}
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
	material := domain + "\x00" + string(endpoint)
	if image != "" {
		material += "\x00" + image
	}
	sum := sha256.Sum256([]byte(material))
	discovery := Discovery{domain: domain, endpoint: endpoint, image: image, key: hex.EncodeToString(sum[:discoveryKeyBytes])}
	if len(discovery.SocketPath()) > maxSocketPathBytes {
		return Discovery{}, refuse(RefusalSocketPathTooLong, nil)
	}
	return discovery, nil
}

// validImage accepts the empty legacy image and a bounded lowercase hex token.
func validImage(image string) bool {
	if len(image) > maxImageBytes {
		return false
	}
	for _, r := range image {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Endpoint returns the endpoint this discovery scopes.
func (d Discovery) Endpoint() EndpointKey { return d.endpoint }

// Image returns the executable image token this discovery scopes, or empty for
// the legacy contract.
func (d Discovery) Image() string { return d.image }

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
	Image       string      `json:"image,omitempty"`
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
	if record.Endpoint != discovery.endpoint || record.Image != discovery.image || strings.TrimSpace(record.Runtime) == "" ||
		strings.TrimSpace(record.Credential) == "" {
		return discoveryRecord{}, refuse(RefusalDiscoveryUntrusted, nil)
	}
	return record, nil
}

// PublishedRuntimeID reads the runtime identity from an ownership-checked
// discovery record. It observes publication without dialing or exposing the
// credential; a caller still uses Dial to authenticate a live connection.
func PublishedRuntimeID(discovery Discovery) (string, error) {
	record, err := readRecord(discovery)
	if err != nil {
		return "", err
	}
	return record.Runtime, nil
}

// Published lists the runtimes this state domain has an ownership-checked
// record for, in directory order. It creates nothing and repairs nothing: an
// absent directory is an empty result, and a record that does not derive back
// to its own location is skipped rather than removed.
func Published(stateDomain string) ([]Discovery, error) {
	locator, err := NewDiscovery(stateDomain, DefaultEndpointKey)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(locator.Dir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, refuse(RefusalDiscoveryUntrusted, err)
	}
	var published []Discovery
	for _, entry := range entries {
		if len(published) >= maxPublishedRecords {
			break
		}
		name := entry.Name()
		if !entry.Type().IsRegular() || !strings.HasPrefix(name, discoveryPrefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		var header struct {
			Endpoint EndpointKey `json:"endpoint"`
			Image    string      `json:"image"`
		}
		path := filepath.Join(locator.Dir(), name)
		payload, err := readBounded(path)
		if err != nil || json.Unmarshal(payload, &header) != nil {
			continue
		}
		discovery, err := NewImageDiscovery(locator.domain, header.Endpoint, header.Image)
		if err != nil || discovery.RecordPath() != path {
			continue
		}
		if _, err := readRecord(discovery); err != nil {
			continue
		}
		published = append(published, discovery)
	}
	return published, nil
}

// LocateRuntime returns the discovery contract of the published runtime with
// this exact identity on this endpoint, whichever image it serves.
//
// It is how a caller holding authority a runtime already granted reaches that
// runtime again. Deriving the contract from the caller's own image instead
// would send an existing binding to whichever runtime this executable starts,
// which never granted it.
func LocateRuntime(stateDomain string, endpoint EndpointKey, runtimeID string) (Discovery, error) {
	if strings.TrimSpace(runtimeID) == "" {
		return Discovery{}, refuse(RefusalRuntimeReplaced, nil)
	}
	published, err := Published(stateDomain)
	if err != nil {
		return Discovery{}, err
	}
	for _, discovery := range published {
		if discovery.endpoint != endpoint {
			continue
		}
		if record, err := readRecord(discovery); err == nil && record.Runtime == runtimeID {
			return discovery, nil
		}
	}
	return Discovery{}, refuse(RefusalHostUnavailable, nil)
}

// readBounded reads one owner-private regular record file within the record
// bound.
func readBounded(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ownedByCurrentUser(info) {
		return nil, refuse(RefusalDiscoveryUntrusted, nil)
	}
	file, err := os.Open(path) // #nosec G304 -- path is one entry of the owner-private discovery directory.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil || len(payload) > maxRecordBytes {
		return nil, refuse(RefusalDiscoveryUntrusted, err)
	}
	return payload, nil
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
