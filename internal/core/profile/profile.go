// Package profile owns the named Agent profiles a user can keep.
//
// A profile is a small TOML file naming how an Agent should be started: the
// provider it is for, which stored instructions it gets, its model and
// effort, the roles it serves, and its permissions. This package is the only owner of where profile files live
// (<ConfigDir>/profiles/<name>.toml), what a profile may be named and contain,
// how its content is identified (the digest), and which built-in profiles are
// compiled into the binary. It stores and validates profiles only; nothing
// here applies one to an Agent.
package profile

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/state"
)

// MaxSize is the largest profile file accepted, in bytes (64 KiB).
const MaxSize = 64 * 1024

// DirName is the directory below ConfigDir that holds profile files.
const DirName = "profiles"

// FileExt is the extension of every profile file.
const FileExt = ".toml"

// ReservedName is the one otherwise-valid name no profile may take: it is
// kept free to spell "no profile".
const ReservedName = "none"

// Sources of a listed profile.
const (
	SourceBuiltin = "builtin"
	SourceUser    = "user"
)

// Refusal reason tokens. They are stable strings: every refusal this package
// reports carries exactly one of them in its error text.
const (
	ReasonNameInvalid  = "profile-name-invalid"
	ReasonNameReserved = "profile-name-reserved"
	ReasonNotFound     = "profile-not-found"
	ReasonTooLarge     = "profile-too-large"
	ReasonSyntax       = "profile-syntax-invalid"
	ReasonKeyUnknown   = "profile-key-unknown"
	ReasonTableUnknown = "profile-table-unknown"
	ReasonValueInvalid = "profile-value-invalid"
	// ReasonInstructionsNotFound is an `instructions` value that names no
	// stored instructions file.
	ReasonInstructionsNotFound = "profile-instructions-not-found"
	// ReasonRoleDuplicate is a role listed twice in one file.
	ReasonRoleDuplicate = "profile-role-duplicate"
	// ReasonRoleClaimed is a role another profile already lists.
	ReasonRoleClaimed = "profile-role-claimed"
	// ReasonRoleProfileInvalid is a role whose one listing profile is
	// invalid. The role selects nothing rather than fall back to no profile.
	ReasonRoleProfileInvalid = "profile-role-profile-invalid"
	// ReasonProviderUnknown is a `provider` value that names no Agent
	// provider.
	ReasonProviderUnknown = "profile-provider-unknown"
	// ReasonInstructionsInUse is a delete of stored instructions that a user
	// profile names.
	ReasonInstructionsInUse = "profile-instructions-in-use"
	// ReasonBuiltin is a delete of a profile that exists only as a builtin.
	ReasonBuiltin = "profile-builtin"
)

// Error is a profile refusal. Reason is one of the Reason* tokens.
type Error struct {
	Reason string
	Name   string
	Detail string
}

func (e *Error) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("%s: %s", e.Reason, e.Detail)
	}
	return fmt.Sprintf("%s: profile %q %s", e.Reason, e.Name, e.Detail)
}

// ReasonOf returns the refusal token carried by err, or "" when err is not a
// profile refusal.
func ReasonOf(err error) string {
	var refusal *Error
	if errors.As(err, &refusal) {
		return refusal.Reason
	}
	return ""
}

// named returns err with name filled in when it is a nameless refusal.
func named(err error, name string) error {
	var refusal *Error
	if errors.As(err, &refusal) && refusal.Name == "" {
		refusal.Name = name
	}
	return err
}

// ValidateName applies the persona name rule (a valid Projmux resource name
// that does not start with "." or "-") and refuses the reserved name "none".
func ValidateName(name string) error {
	if err := persona.ValidateName(name); err != nil {
		detail := err.Error()
		var refusal *persona.Error
		if errors.As(err, &refusal) {
			detail = refusal.Detail
		}
		return &Error{Reason: ReasonNameInvalid, Name: name, Detail: detail}
	}
	if name == ReservedName {
		return &Error{Reason: ReasonNameReserved, Name: name, Detail: "is reserved"}
	}
	return nil
}

// Digest returns the profile digest of content: sha256:<lowercase hex> over the
// raw bytes, the same spelling persona digests use.
func Digest(content []byte) string { return persona.Digest(content) }

// builtins are the profiles compiled into the binary, by name. A user file of
// the same name shadows one. Each must pass Parse (a test holds that).
var builtins = map[string]string{
	// readonly lets an Agent read and never write. It claims no role.
	"readonly": `[permissions]
sandbox = "read-only"
approval = "never"
deny = ["Edit", "Write", "NotebookEdit"]
`,
}

// Profile is one profile's name, where it came from, and its exact content.
type Profile struct {
	Name    string
	Source  string
	Content []byte
	Digest  string
}

// Entry describes one listed profile. An invalid profile is still listed:
// Valid is false and Reason/Detail say why. A profile that parses keeps its
// provider, instructions, model, effort, and roles even when it is invalid, so
// its roles still count and a listing still shows what it names; a file that
// does not parse has none of them.
type Entry struct {
	Name         string
	Source       string
	Path         string
	Provider     string
	Instructions string
	Model        string
	Effort       string
	Roles        []string
	Digest       string
	Valid        bool
	Reason       string
	Detail       string
}

// withSpec fills the items of spec the entry shows.
func (e Entry) withSpec(spec Spec) Entry {
	e.Provider, e.Instructions, e.Model, e.Effort, e.Roles = spec.Provider, spec.Instructions, spec.Model, spec.Effort, spec.Roles
	return e
}

// Store reads and writes profile files below <ConfigDir>/profiles and checks
// `instructions` against the persona store.
//
// A builtin-only store (NewBuiltinStore) has no directory: it lists and loads
// only the builtins, touches no file, and refuses every write with its reason.
type Store struct {
	dir      string
	personas persona.Store
	// unavailable is why a builtin-only store has no directory.
	unavailable error
}

// NewStore builds a store over configDir/profiles, resolving `instructions`
// through the persona store of configDir and stateDir.
func NewStore(configDir, stateDir string) Store {
	return Store{dir: filepath.Join(configDir, DirName), personas: persona.NewStore(configDir, stateDir)}
}

// NewDefaultStore builds a store from resolved projmux paths.
func NewDefaultStore(paths config.Paths) Store {
	return NewStore(paths.ConfigDir, paths.StateDir)
}

// NewBuiltinStore builds the builtin-only store a read uses when the config
// directory cannot be resolved; reason, such as a config.MissingHomeError,
// says why and is what its writes return.
func NewBuiltinStore(reason error) Store {
	if reason == nil {
		reason = config.ErrHomeDirRequired
	}
	return Store{unavailable: reason}
}

// Path returns the file path of the user profile named name. A builtin-only
// store has none and returns its reason.
func (s Store) Path(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	if s.dir == "" {
		return "", s.unavailable
	}
	return filepath.Join(s.dir, name+FileExt), nil
}

// Load returns one profile exactly as stored: the user file when there is
// one, else the builtin of that name. Neither is profile-not-found.
func (s Store) Load(name string) (Profile, error) {
	if err := ValidateName(name); err != nil {
		return Profile{}, err
	}
	if s.dir != "" {
		content, found, err := readUserFile(s.dir, name)
		if err != nil {
			return Profile{}, named(err, name)
		}
		if found {
			return Profile{Name: name, Source: SourceUser, Content: content, Digest: Digest(content)}, nil
		}
	}
	if raw, ok := builtins[name]; ok {
		return Profile{Name: name, Source: SourceBuiltin, Content: []byte(raw), Digest: Digest([]byte(raw))}, nil
	}
	if s.dir == "" {
		return Profile{}, &Error{Reason: ReasonNotFound, Name: name, Detail: "is not a builtin, and no user profile can be read: " + s.unavailable.Error()}
	}
	return Profile{}, &Error{Reason: ReasonNotFound, Name: name, Detail: "does not exist at " + filepath.Join(s.dir, name+FileExt) + " and is not a builtin"}
}

// ReadLimited reads r to the end, refusing content larger than MaxSize with
// profile-too-large. It never reads more than MaxSize+1 bytes.
func ReadLimited(r io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxSize {
		return nil, &Error{Reason: ReasonTooLarge, Detail: fmt.Sprintf("is more than %d bytes", MaxSize)}
	}
	return content, nil
}

// validate is the full check of one profile's content: Parse, then the named
// instructions must exist in the persona store.
func (s Store) validate(content []byte) (Spec, error) {
	spec, err := Parse(content)
	if err != nil {
		return Spec{}, err
	}
	if err := s.checkInstructions(spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

// checkInstructions refuses a parsed profile whose `instructions` name no
// usable stored instructions.
func (s Store) checkInstructions(spec Spec) error {
	if spec.Instructions == "" {
		return nil
	}
	if _, err := s.personas.Load(spec.Instructions); err != nil {
		switch persona.ReasonOf(err) {
		case "":
			return err
		case persona.ReasonNotFound:
			return &Error{Reason: ReasonInstructionsNotFound, Detail: fmt.Sprintf("names instructions %q, which do not exist", spec.Instructions)}
		default:
			return &Error{Reason: ReasonValueInvalid, Detail: fmt.Sprintf("names unusable instructions %q: %v", spec.Instructions, err)}
		}
	}
	return nil
}

// Write validates content as the profile named name and, only when it is
// valid, replaces the user file atomically (file 0600, directory 0700). A
// refusal writes nothing and leaves an existing file as it was. A role that
// another profile already lists is refused, whether that profile is valid or
// not: an invalid profile that parses still holds its roles, the way List and
// RoleProfile count them. The profile being replaced, and the builtin a user
// file of this name would shadow, do not count.
func (s Store) Write(name string, content []byte) (Entry, error) {
	path, err := s.Path(name)
	if err != nil {
		return Entry{}, err
	}
	if len(content) > MaxSize {
		return Entry{}, &Error{Reason: ReasonTooLarge, Name: name, Detail: fmt.Sprintf("is %d bytes; the limit is %d bytes", len(content), MaxSize)}
	}
	spec, err := s.validate(content)
	if err != nil {
		return Entry{}, named(err, name)
	}
	others, err := s.collect()
	if err != nil {
		return Entry{}, err
	}
	for _, other := range others {
		if other.Name == name {
			continue
		}
		for _, role := range spec.Roles {
			if !slices.Contains(other.Roles, role) {
				continue
			}
			detail := fmt.Sprintf("lists role %q, which %s profile %q already lists", role, other.Source, other.Name)
			if !other.Valid {
				detail += fmt.Sprintf(" (that profile is invalid: %s)", other.Reason)
			}
			return Entry{}, &Error{Reason: ReasonRoleClaimed, Name: name, Detail: detail}
		}
	}
	if err := writeAtomic(path, content); err != nil {
		return Entry{}, fmt.Errorf("write profile %q: %w", name, err)
	}
	return Entry{Name: name, Source: SourceUser, Path: path, Digest: Digest(content), Valid: true}.withSpec(spec), nil
}

// Delete removes the user profile named name. A name that is only a builtin
// is refused with profile-builtin; deleting a user file that shadows a builtin
// makes the builtin visible again.
func (s Store) Delete(name string) error {
	path, err := s.Path(name)
	if err != nil {
		return err
	}
	missing := func() error {
		if _, ok := builtins[name]; ok {
			return &Error{Reason: ReasonBuiltin, Name: name, Detail: "is built in and cannot be deleted; only a user file of the same name can"}
		}
		return &Error{Reason: ReasonNotFound, Name: name, Detail: "does not exist at " + path}
	}
	root, err := os.OpenRoot(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return missing()
	}
	if err != nil {
		return fmt.Errorf("delete profile %q: %w", name, err)
	}
	defer root.Close()
	fileName := name + FileExt
	info, err := root.Lstat(fileName)
	if errors.Is(err, fs.ErrNotExist) {
		return missing()
	}
	if err != nil {
		return fmt.Errorf("delete profile %q: %w", name, err)
	}
	if info.IsDir() {
		return &Error{Reason: ReasonNotFound, Name: name, Detail: "is not a regular file at " + path}
	}
	if err := root.Remove(fileName); err != nil {
		return fmt.Errorf("delete profile %q: %w", name, err)
	}
	return nil
}

// List returns every profile sorted by name: the builtins and the user files,
// a user file winning over a builtin of the same name. Each entry is checked
// on its own (Parse and the instructions lookup), and then a role listed by
// more than one profile that parses -- valid or not -- marks each of them
// that is otherwise valid invalid with profile-role-claimed; one already
// invalid keeps its own reason. An invalid file is listed as invalid and never stops
// the others from listing. Files whose name is not a valid profile name,
// including the hidden temporary files of an in-flight Write, are skipped.
func (s Store) List() ([]Entry, error) {
	entries, err := s.collect()
	if err != nil {
		return nil, err
	}
	claimants := map[string][]int{}
	for i, entry := range entries {
		for _, role := range entry.Roles {
			claimants[role] = append(claimants[role], i)
		}
	}
	for role, indexes := range claimants {
		if len(indexes) < 2 {
			continue
		}
		for _, i := range indexes {
			if !entries[i].Valid {
				continue
			}
			entries[i].Valid = false
			entries[i].Reason = ReasonRoleClaimed
			entries[i].Detail = fmt.Sprintf("role %q is listed by more than one profile", role)
		}
	}
	return entries, nil
}

// collect reads and checks every profile on its own, without the
// cross-profile role check.
func (s Store) collect() ([]Entry, error) {
	byName := map[string]Entry{}
	for name, raw := range builtins {
		byName[name] = s.describe(Entry{Name: name, Source: SourceBuiltin}, []byte(raw))
	}
	var dirEntries []os.DirEntry
	if s.dir != "" {
		var err error
		if dirEntries, err = os.ReadDir(s.dir); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("list profiles: %w", err)
		}
	}
	for _, dirEntry := range dirEntries {
		name, ok := strings.CutSuffix(dirEntry.Name(), FileExt)
		if !ok || ValidateName(name) != nil {
			continue
		}
		entry := Entry{Name: name, Source: SourceUser, Path: filepath.Join(s.dir, dirEntry.Name())}
		content, found, err := readUserFile(s.dir, name)
		if err != nil {
			if ReasonOf(err) == "" {
				return nil, fmt.Errorf("list profiles: %w", err)
			}
			entry.Reason, entry.Detail = ReasonOf(err), err.Error()
			byName[name] = entry
			continue
		}
		if !found {
			continue
		}
		byName[name] = s.describe(entry, content)
	}
	entries := make([]Entry, 0, len(byName))
	for _, entry := range byName {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// describe fills the digest, the named items, and validity of one profile.
// A profile that parses keeps its items even when its instructions are
// missing; one that does not parse has none.
func (s Store) describe(entry Entry, content []byte) Entry {
	entry.Digest = Digest(content)
	spec, err := Parse(content)
	if err == nil {
		entry = entry.withSpec(spec)
		err = s.checkInstructions(spec)
	}
	if err != nil {
		entry.Reason = ReasonOf(err)
		if entry.Reason == "" {
			entry.Reason = ReasonValueInvalid
		}
		entry.Detail = named(err, entry.Name).Error()
		return entry
	}
	entry.Valid = true
	return entry
}

// RoleProfile returns the name of the profile a `role` creation label
// selects: the one profile listing role, when it is valid. A role no profile
// lists selects none and returns "". Every profile that parses counts,
// invalid ones included, so a role never silently selects nothing because
// the profile that lists it broke: a role several profiles list is
// profile-role-claimed, and a role whose one listing profile is invalid is
// profile-role-profile-invalid, carrying that profile's own reason.
func (s Store) RoleProfile(role string) (string, error) {
	entries, err := s.List()
	if err != nil {
		return "", err
	}
	var listing []Entry
	for _, entry := range entries {
		if slices.Contains(entry.Roles, role) {
			listing = append(listing, entry)
		}
	}
	switch {
	case len(listing) == 0:
		return "", nil
	case len(listing) > 1:
		names := make([]string, 0, len(listing))
		for _, entry := range listing {
			names = append(names, entry.Name)
		}
		return "", &Error{Reason: ReasonRoleClaimed,
			Detail: fmt.Sprintf("role %q is listed by more than one profile (%s)", role, strings.Join(names, ", "))}
	case !listing[0].Valid:
		return "", &Error{Reason: ReasonRoleProfileInvalid, Name: listing[0].Name,
			Detail: fmt.Sprintf("lists role %q but is invalid: %s", role, listing[0].Detail)}
	}
	return listing[0].Name, nil
}

// ProfilesUsingInstructions returns, sorted, the names of the user profiles
// whose `instructions` name the stored instructions called name. Every user
// file that parses counts, valid or not; a file that does not parse names
// nothing. Builtins name no instructions.
func (s Store) ProfilesUsingInstructions(name string) ([]string, error) {
	entries, err := s.collect()
	if err != nil {
		return nil, err
	}
	var users []string
	for _, entry := range entries {
		if entry.Source == SourceUser && entry.Instructions == name {
			users = append(users, entry.Name)
		}
	}
	return users, nil
}

// readUserFile reads <dir>/<name>.toml through an os.Root, so the open cannot
// leave dir. A missing directory or file, or a path that is not a regular
// file, is found=false. Content over MaxSize is profile-too-large.
func readUserFile(dir, name string) ([]byte, bool, error) {
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	file, err := root.Open(name + FileExt)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil
	}
	content, err := ReadLimited(file)
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

// writeAtomic writes content to a hidden temporary file beside path and
// renames it into place, as the persona store does. The directory is created
// 0700 and the file is 0600.
func writeAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := state.EnsurePrivateDir(dir); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(state.PrivateFileMode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		return err
	}
	committed = true
	state.RepairPrivateFile(path)
	return nil
}
