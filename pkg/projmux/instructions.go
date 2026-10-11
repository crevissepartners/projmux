package projmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/services/instructions"
	"github.com/crevissepartners/projmux/internal/services/questions"
)

// Options selects captured HOME/XDG defaults and explicit path overrides.
// ConfigDir is the projmux directory itself, not its XDG parent. Profiles used
// to guard Delete live in ConfigDir too. No method changes global environment.
type Options struct {
	// ApprovalControl overrides the exact existing Codex approval transport.
	ApprovalControl ApprovalControl
	// QuestionDispatcher overrides only the existing process question transport.
	QuestionDispatcher QuestionDispatcher

	ConfigDir string
	// StateDir is the absolute projmux state directory itself. Empty captures
	// HOME/XDG defaults at construction; no Registry path or writer is exposed.
	StateDir string
	// SocketPath selects one exact tmux server. Empty observes the app socket
	// named projmux for resource reads. ObserveRuntime instead selects no server
	// when empty. TMUX is never used.
	SocketPath string
	// CodexModelLister overrides the same bounded model reader the CLI uses.
	// Nil uses the existing default app-server endpoint. The callback must honor
	// context cancellation and return suggestion order without starting a daemon.
	CodexModelLister func(context.Context) ([]string, error)
}

// Client executes public instruction, profile, and read-only operations. It owns no background work and needs
// no Close. Its paths are immutable; methods may be called concurrently. Writes
// are atomic, but multi-call transactions and concurrent profile edits are not
// serialized (the same limits apply to separate CLI processes).
type Client struct {
	approvalControl    ApprovalControl
	questionDispatcher questions.Dispatcher
	usageDir           string
	instructions       instructions.Service
	paths              config.Paths
	pathsErr           error
	transport          resourcegraph.Transport
	runtimeSocketPath  string
	codexModelLister   func(context.Context) ([]string, error)
}

// NewClient captures paths without creating directories or changing the process
// environment. An explicit ConfigDir must be absolute.
func NewClient(options Options) (*Client, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil && options.ConfigDir == "" && options.StateDir == "" {
		return nil, err
	}
	pathsErr := err
	for _, override := range []struct{ name, path string }{{"ConfigDir", options.ConfigDir}, {"StateDir", options.StateDir}, {"SocketPath", options.SocketPath}} {
		if override.path != "" && !filepath.IsAbs(override.path) {
			return nil, fmt.Errorf("%s must be absolute", override.name)
		}
	}
	if options.ConfigDir != "" {
		paths.ConfigDir = filepath.Clean(options.ConfigDir)
	}
	if options.StateDir != "" {
		paths.StateDir = filepath.Clean(options.StateDir)
		pathsErr = nil
	}
	transport := resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "projmux", Source: resourcegraph.TransportSourceAppSocket}
	if options.SocketPath != "" {
		transport = resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: filepath.Clean(options.SocketPath), Source: resourcegraph.TransportSourceSocketPath}
	}
	usageOverride := strings.TrimSpace(os.Getenv("PROJMUX_USAGE_STATE_DIR"))
	if options.StateDir != "" {
		usageOverride = ""
	}
	var dispatcher questions.Dispatcher
	if options.QuestionDispatcher != nil {
		dispatcher = publicQuestionDispatcher{options.QuestionDispatcher}
	}
	return &Client{approvalControl: options.ApprovalControl, questionDispatcher: dispatcher, usageDir: defaultUsageDir(paths.StateDir, usageOverride), instructions: instructions.Service{Paths: func() (config.Paths, error) {
		if paths.ConfigDir == "" {
			return config.Paths{}, err
		}
		return paths, nil
	}}, paths: paths, pathsErr: pathsErr, transport: transport, runtimeSocketPath: options.SocketPath, codexModelLister: options.CodexModelLister}, nil
}

// Code is a machine-readable refusal reason. Instruction and profile codes retain CLI
// reason tokens; resource codes classify the existing CLI failure kinds.
type Code string

const (
	CodeNameInvalid       Code = "persona-name-invalid"
	CodeNotFound          Code = "persona-not-found"
	CodeTooLarge          Code = "persona-too-large"
	CodeInstructionsInUse Code = "profile-instructions-in-use"
)

// Refusal describes a rejected operation. Use errors.As and Code, rather than
// matching Error text. Other filesystem errors and context cancellation remain
// ordinary errors. Users names the profiles blocking a deletion.
type Refusal struct {
	Code    Code
	Name    string
	Users   []string
	message string
}

func (e *Refusal) Error() string { return e.message }

// InstructionInfo describes a stored file. Size and Digest cover its raw bytes.
type InstructionInfo struct {
	Name    string
	Path    string
	Digest  string
	Size    int64
	ModTime time.Time
}

// InstructionContent holds exact stored bytes with no newline normalization.
// Content belongs to the caller.
type InstructionContent struct {
	Name    string
	Content []byte
	Digest  string
}

// InstructionWriteResult describes the atomically replaced file.
type InstructionWriteResult struct {
	Name    string
	Path    string
	Digest  string
	Size    int64
	ModTime time.Time
}

// ListInstructions returns files sorted by name; a missing directory is empty.
func (c *Client) ListInstructions(ctx context.Context) ([]InstructionInfo, error) {
	entries, err := c.instructions.List(ctx)
	if err != nil {
		return nil, refusal(err)
	}
	result := make([]InstructionInfo, len(entries))
	for i, entry := range entries {
		result[i] = InstructionInfo{Name: entry.Name, Path: entry.Path, Digest: entry.Digest, Size: entry.Size, ModTime: entry.ModTime}
	}
	return result, nil
}

// ReadInstructions reads a named regular file, limited to 64 KiB.
func (c *Client) ReadInstructions(ctx context.Context, name string) (InstructionContent, error) {
	loaded, err := c.instructions.Read(ctx, name)
	if err != nil {
		return InstructionContent{}, refusal(err)
	}
	return InstructionContent{Name: loaded.Name, Content: loaded.Content, Digest: loaded.Digest}, nil
}

// WriteInstructions atomically stores content (at most 64 KiB), creating a 0700
// personas directory and a 0600 file. A context canceled before admission writes
// nothing; after admission the atomic operation finishes even if canceled.
func (c *Client) WriteInstructions(ctx context.Context, name string, content []byte) (InstructionWriteResult, error) {
	entry, err := c.instructions.Write(ctx, name, content)
	if err != nil {
		return InstructionWriteResult{}, refusal(err)
	}
	return InstructionWriteResult{Name: entry.Name, Path: entry.Path, Digest: entry.Digest, Size: entry.Size, ModTime: entry.ModTime}, nil
}

// DeleteInstructions deletes a named file, refusing profiles that use it. Calling
// this method explicitly authorizes deletion (the CLI additionally asks --yes).
// Snapshots and already running Agents are unaffected.
func (c *Client) DeleteInstructions(ctx context.Context, name string) error {
	return refusal(c.instructions.Delete(ctx, name))
}

func refusal(err error) error {
	if err == nil {
		return nil
	}
	var stored *persona.Error
	if errors.As(err, &stored) {
		return &Refusal{Code: Code(stored.Reason), Name: stored.Name, message: err.Error()}
	}
	var used *instructions.InUseError
	if errors.As(err, &used) {
		return &Refusal{Code: Code(profile.ReasonInstructionsInUse), Name: used.Name, Users: append([]string(nil), used.Users...), message: err.Error()}
	}
	return err
}
