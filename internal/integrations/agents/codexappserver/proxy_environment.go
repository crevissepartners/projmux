package codexappserver

import (
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
)

// ProxyEnvironment is a captured provider environment, independent of Projmux
// state routing. It contains no credentials and owns no daemon lifecycle.
type ProxyEnvironment struct {
	Executable string
	HomeDir    string
	Values     map[string]string
}

func (e *ProxyEnvironment) lookPath(name string) (string, error) {
	if e == nil {
		return exec.LookPath(name)
	}
	if name != "codex" || !filepath.IsAbs(e.Executable) {
		return "", exec.ErrNotFound
	}
	info, err := os.Stat(e.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", exec.ErrNotFound
	}
	return e.Executable, nil
}
func (e *ProxyEnvironment) environ() []string {
	if e == nil {
		return nil
	}
	values := make(map[string]string, len(e.Values)+1)
	maps.Copy(values, e.Values)
	values["HOME"] = e.HomeDir
	out := make([]string, 0, len(values))
	for k, v := range values {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
func (e *ProxyEnvironment) InstallCapability() InstallCapability {
	if e == nil {
		return ObserveDefaultInstallCapability()
	}
	return observeInstallCapability(func(k string) string { return e.Values[k] }, func() (string, error) { return e.HomeDir, nil }, e.lookPath, os.Stat, runtime.GOOS)
}
func (e *ProxyEnvironment) daemonNotRunning() bool {
	if e == nil {
		return defaultDaemonNotRunning()
	}
	root := e.Values["CODEX_HOME"]
	if root == "" {
		root = filepath.Join(e.HomeDir, ".codex")
	}
	if !filepath.IsAbs(root) {
		return false
	}
	return daemonNotRunningAt(filepath.Join(root, "app-server-control", "app-server-control.sock"), os.Lstat, net.DialTimeout)
}
