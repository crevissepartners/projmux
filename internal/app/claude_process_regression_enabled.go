//go:build projmux_process_cli_regression

package app

// This gate is compiled only into the CLI fixture's additional binary. It
// delays one already-validated response; it cannot replace an admission result.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
)

type claudeRegressionArm struct {
	Digest       string
	BinarySHA256 string
	Until        time.Time
}

type claudeRegressionSelection struct {
	Digest          string
	BinarySHA256    string
	Host            coremetadata.ProcessIdentity
	Provider        coremetadata.ProcessIdentity
	Helper          coremetadata.ProcessIdentity
	AuthoritySHA256 string
}

type claudeRegressionNative struct {
	Selection   claudeRegressionSelection
	ObserverPID int
	Kind        string
}

type claudeRegressionClient struct {
	Digest       string
	BinarySHA256 string
	PID          int
}

type claudeRegressionControl struct {
	root *os.Root
	arm  claudeRegressionArm
}

func claudeRegressionHash(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func claudeRegressionProofDigest(proof claudeProcessProof) string {
	return claudeRegressionHash([4]any{proof.Binding, proof.Process, proof.HostProcess, proof.Session})
}

func openClaudeRegressionControl(proof claudeProcessProof) *claudeRegressionControl {
	path := os.Getenv("PMX_TEST_CLAUDE_REGRESSION_CONTROL")
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !localipc.OwnedByCurrentUser(info) {
		return nil
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil
	}
	c := &claudeRegressionControl{root: root}
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(info, current) || !c.read("arm.json", &c.arm) || c.arm.Digest != claudeRegressionProofDigest(proof) || len(c.arm.BinarySHA256) != 64 || !time.Now().Before(c.arm.Until) || time.Until(c.arm.Until) > 40*time.Second {
		root.Close()
		return nil
	}
	return c
}

func (c *claudeRegressionControl) read(name string, value any) bool {
	before, err := c.root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || !localipc.OwnedByCurrentUser(before) || before.Size() > 2048 {
		return false
	}
	file, err := c.root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	pin, err := file.Stat()
	if err != nil || !os.SameFile(before, pin) {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(file, 2049))
	if err != nil || len(raw) > 2048 {
		return false
	}
	after, err := c.root.Lstat(name)
	if err != nil || !os.SameFile(pin, after) || pin.Size() != after.Size() || pin.ModTime() != after.ModTime() {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil {
		return false
	}
	return decoder.Decode(new(any)) == io.EOF
}

// No JSON field contains Registry, environment, argv, token, or error bodies.
func (c *claudeRegressionControl) write(name string, value any) bool {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 2048 {
		return false
	}
	if _, err := c.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	pending := name + ".pending"
	file, err := c.root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return false
	}
	defer c.root.Remove(pending)
	_, writeErr := file.Write(raw)
	closeErr := file.Close()
	return writeErr == nil && closeErr == nil && c.root.Rename(pending, name) == nil
}

func (c *claudeRegressionControl) sameExecutable() bool {
	path, err := os.Executable()
	if err != nil {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return false
	}
	return hex.EncodeToString(sum.Sum(nil)) == c.arm.BinarySHA256
}

func observeClaudeHostCheckForRegression(s *claudeProcessService, request claudeProcessCheck, peer coremetadata.ProcessIdentity, before *coremetadata.ClaudeRegistration, result claudeProcessCheckResult) {
	proof := claudeProcessProof{Binding: s.binding, Process: s.ownedProcess, HostProcess: s.ownedHostProcess, Session: request.Session}
	c := openClaudeRegressionControl(proof)
	if c == nil {
		return
	}
	defer c.root.Close()
	if !result.Valid || request.Binding != s.binding || os.Getpid() != s.ownedHostProcess.PID {
		return
	}
	if request.Helper != nil && request.Helper.Phase == "clear" && before != nil && result.Registration == nil && request.Helper.Registration.Authority == before.Authority {
		var selected claudeRegressionSelection
		if c.read("selected.json", &selected) && selected.Helper == peer && selected.AuthoritySHA256 == claudeRegressionHash(before.Authority) && c.sameExecutable() {
			c.write("cleared.json", selected)
		}
		return
	}
	if request.Register || request.Lookup || request.Helper != nil || request.Input != nil || result.Registration == nil || !result.Registration.Ready || result.Registration.Authority.LeaseProcess != peer || !c.sameExecutable() {
		return
	}
	selected := claudeRegressionSelection{Digest: c.arm.Digest, BinarySHA256: c.arm.BinarySHA256, Host: s.ownedHostProcess, Provider: s.ownedProcess, Helper: peer, AuthoritySHA256: claudeRegressionHash(result.Registration.Authority)}
	if !c.write("selected.json", selected) {
		return
	}
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	deadline := time.NewTimer(time.Until(c.arm.Until))
	defer deadline.Stop()
	for {
		var release claudeRegressionSelection
		if c.read("release.json", &release) && release == selected {
			c.write("released.json", selected)
			return
		}
		select {
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}

// This Reader observes each original Read result and returns it unchanged.
// In particular, a partial frame plus a deadline is never decoded as success.
type claudeRegressionReader struct {
	io.Reader
	control *claudeRegressionControl
	proof   claudeProcessProof
}

func (r claudeRegressionReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		var selected claudeRegressionSelection
		if r.control.read("selected.json", &selected) && selected.Digest == r.control.arm.Digest && selected.Host == r.proof.HostProcess && selected.Provider == r.proof.Process && selected.Helper.PID == os.Getpid() {
			r.control.write("native.json", claudeRegressionNative{Selection: selected, ObserverPID: os.Getpid(), Kind: "deadline"})
		}
	}
	return n, err
}

func readClaudeHostResponseForRegression(conn *net.UnixConn, result *claudeProcessCheckResult, proof claudeProcessProof, request claudeProcessCheck) error {
	if request.Register || request.Lookup || request.Helper != nil || request.Input != nil {
		return localipc.ReadJSON(conn, result)
	}
	c := openClaudeRegressionControl(proof)
	if c == nil {
		return localipc.ReadJSON(conn, result)
	}
	defer c.root.Close()
	if !c.sameExecutable() {
		return localipc.ReadJSON(conn, result)
	}
	client := claudeRegressionClient{Digest: c.arm.Digest, BinarySHA256: c.arm.BinarySHA256, PID: os.Getpid()}
	if !c.write("client.json", client) {
		var done claudeRegressionClient
		if !c.read("client-done.json", &done) {
			c.write("ambiguous.json", client)
		}
		return localipc.ReadJSON(conn, result)
	}
	defer c.write("client-done.json", client)
	return localipc.ReadJSON(claudeRegressionReader{Reader: conn, control: c, proof: proof}, result)
}

func observeClaudeHelperRetainedForRegression(bootstrap claudeEndpointBootstrap) {
	if bootstrap.ProcessProof == nil {
		return
	}
	c := openClaudeRegressionControl(*bootstrap.ProcessProof)
	if c == nil {
		return
	}
	defer c.root.Close()
	var selected claudeRegressionSelection
	if c.read("selected.json", &selected) && selected.Helper == bootstrap.Registration.Authority.LeaseProcess && selected.AuthoritySHA256 == claudeRegressionHash(bootstrap.Registration.Authority) && selected.Helper.PID == os.Getpid() && c.sameExecutable() {
		c.write("retained.json", selected)
	}
}
