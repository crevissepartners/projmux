// Package codexinstalled owns the test-only contract for qualifications that
// execute a real installed Codex app-server. It deliberately retains only
// content-free version, topology, stage, class, reason, and command semantics.
package codexinstalled

import (
	"encoding/json"
	"fmt"
	"strings"
)

const UnknownVersion = "unknown"

type Topology string

const (
	TopologyDirect  Topology = "direct"
	TopologyManaged Topology = "managed"
)

type Stage string

const (
	StageProvision Stage = "provision"
	StageStart     Stage = "start"
	StageReady     Stage = "ready"
	StageReuse     Stage = "reuse"
	StageClose     Stage = "close"
	StageRetire    Stage = "retire"
)

type ResultClass string

const (
	ResultPass        ResultClass = "pass"
	ResultFail        ResultClass = "fail"
	ResultUnsupported ResultClass = "unsupported"
	ResultInfraError  ResultClass = "infra-error"
)

type VersionTuple struct {
	CLI       string `json:"cli"`
	Managed   string `json:"managed"`
	AppServer string `json:"app_server"`
}

func (versions VersionTuple) normalized() VersionTuple {
	versions.CLI = versionOrUnknown(versions.CLI)
	versions.Managed = versionOrUnknown(versions.Managed)
	versions.AppServer = versionOrUnknown(versions.AppServer)
	return versions
}

type Result struct {
	Versions VersionTuple `json:"versions"`
	Topology Topology     `json:"topology"`
	Stage    Stage        `json:"stage"`
	Class    ResultClass  `json:"class"`
	Reason   string       `json:"reason"`
}

func NewResult(versions VersionTuple, topology Topology, stage Stage, class ResultClass, reason string) Result {
	return Result{
		Versions: versions.normalized(),
		Topology: topology,
		Stage:    stage,
		Class:    class,
		Reason:   strings.TrimSpace(reason),
	}
}

func (result Result) Validate() error {
	if result.Versions != result.Versions.normalized() {
		return fmt.Errorf("installed qualification contains a blank version")
	}
	switch result.Topology {
	case TopologyDirect, TopologyManaged:
	default:
		return fmt.Errorf("installed qualification topology %q is not terminal", result.Topology)
	}
	switch result.Stage {
	case StageProvision, StageStart, StageReady, StageReuse, StageClose, StageRetire:
	default:
		return fmt.Errorf("installed qualification stage %q is not terminal", result.Stage)
	}
	switch result.Class {
	case ResultPass, ResultFail, ResultUnsupported, ResultInfraError:
	default:
		return fmt.Errorf("installed qualification class %q is not terminal", result.Class)
	}
	if result.Reason == "" {
		return fmt.Errorf("installed qualification reason is blank")
	}
	unknownVersion := result.Versions.CLI == UnknownVersion ||
		result.Versions.Managed == UnknownVersion || result.Versions.AppServer == UnknownVersion
	if unknownVersion && result.Class != ResultUnsupported && result.Class != ResultInfraError &&
		!(result.Class == ResultPass && (result.Stage == StageProvision || result.Stage == StageStart)) {
		return fmt.Errorf("installed qualification class %q cannot claim terminal evidence with an unknown version", result.Class)
	}
	return nil
}

func (result Result) JSON() (string, error) {
	if err := result.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(result)
	return string(encoded), err
}

func versionOrUnknown(version string) string {
	if version = strings.TrimSpace(strings.TrimPrefix(version, "codex-cli ")); version != "" {
		return version
	}
	return UnknownVersion
}
