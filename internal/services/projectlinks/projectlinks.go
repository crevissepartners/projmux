// Package projectlinks executes Project link rule management for CLI and SDK.
package projectlinks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	links "github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/internal/core/selector"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

const ExampleValue = "ABC-123"
const CodeInvalid = "invalid-link-rules"
const CodeUnreadable = "link-rules-unreadable"
const CodeConfirmationRequired = "link-rules-confirmation-required"

// Refusal carries a validation field and reason, or an unreadable file reason.
type Refusal struct {
	Code, Field, Reason string
	Cause               error
}

func (e *Refusal) Error() string {
	if e.Field != "" {
		return e.Code + ": " + e.Field + ": " + e.Reason
	}
	return e.Code + ": " + e.Reason
}
func (e *Refusal) Unwrap() error { return e.Cause }

// ProjectError distinguishes Registry/selector errors from storage errors.
type ProjectError struct{ Err error }

func (e *ProjectError) Error() string { return e.Err.Error() }
func (e *ProjectError) Unwrap() error { return e.Err }

// Result describes rules, examples and Project variables; lists are always non-null.
type Result struct {
	Project          string     `json:"project"`
	Jira             []string   `json:"jira"`
	Repo             []string   `json:"repo"`
	URLs             []NamedURL `json:"urls"`
	Links            []Link     `json:"links"`
	ExampleValue     string     `json:"exampleValue"`
	Placeholders     []string   `json:"placeholders"`
	ProjectVariables []Variable `json:"projectVariables"`
	DryRun           bool       `json:"dryRun,omitempty"`
}
type NamedURL struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}
type Link struct {
	LabelKey string `json:"labelKey"`
	Template string `json:"template"`
	Example  string `json:"example,omitempty"`
}
type Variable struct {
	Placeholder string `json:"placeholder"`
	Value       string `json:"value"`
}

// Service captures explicit paths and never infers an active Project.
type Service struct {
	Paths         func() (config.Paths, error)
	RegistryPaths func() (config.Paths, error)
}

// Options selects Registry admission for this call only. The zero value is strict.
type Options struct {
	AllowDegradedRegistry bool
}

func (s Service) target(ctx context.Context, projectRef string, options Options) (links.Store, links.Project, error) {
	if err := ctx.Err(); err != nil {
		return links.Store{}, links.Project{}, err
	}
	ref, err := selector.ParseRef(metadata.KindProject, projectRef)
	if err != nil {
		return links.Store{}, links.Project{}, &ProjectError{err}
	}
	paths, err := s.RegistryPaths()
	if err != nil {
		return links.Store{}, links.Project{}, &ProjectError{err}
	}
	store := storemetadata.NewDefaultStore(paths)
	load := store.LoadReadOnly
	if options.AllowDegradedRegistry {
		load = store.LoadDegradedReadOnly
	}
	registry, err := load()
	if err != nil {
		return links.Store{}, links.Project{}, &ProjectError{&resources.Failure{Code: "registry-unavailable", Cause: err}}
	}
	query := selector.Query{Project: &ref}
	if options.AllowDegradedRegistry {
		if err := uniqueTarget(registry, ref, query); err != nil {
			return links.Store{}, links.Project{}, &ProjectError{err}
		}
	}
	resolved, err := selector.New(registry).ResolveProjects(query)
	if err == nil {
		err = selector.Enforce(selector.Target{Verb: selector.VerbDescribe, Kind: metadata.KindProject}, selector.DescribeSelector(query), resolved)
	}
	if err != nil {
		return links.Store{}, links.Project{}, &ProjectError{err}
	}
	uid := resolved.Matches[0].UID
	if err := links.ValidateProjectUID(uid); err != nil {
		return links.Store{}, links.Project{}, &Refusal{Code: CodeInvalid, Field: "project", Reason: err.Error(), Cause: err}
	}
	found, _ := registry.Project(uid)
	paths, err = s.Paths()
	if err != nil {
		return links.Store{}, links.Project{}, err
	}
	return links.NewDefaultStore(paths), links.ProjectOf(*found), nil
}

// uniqueTarget checks raw rows before the shared selector can deduplicate UIDs.
// A unique name is insufficient when another row carries the selected UID.
func uniqueTarget(registry metadata.Registry, ref selector.Ref, query selector.Query) error {
	raw := selector.Resolution{Kind: metadata.KindProject}
	for _, project := range registry.Projects {
		if (ref.IsUID() && project.Metadata.UID == ref.UID) || (!ref.IsUID() && project.Metadata.Name == ref.Name) {
			raw.Matches = append(raw.Matches, selector.Match{Kind: metadata.KindProject, UID: project.Metadata.UID, Name: project.Metadata.Name})
		}
	}
	target := selector.Target{Verb: selector.VerbDescribe, Kind: metadata.KindProject}
	if err := selector.Enforce(target, selector.DescribeSelector(query), raw); err != nil {
		return err
	}
	uid := raw.Matches[0].UID
	raw.Matches = nil
	for _, project := range registry.Projects {
		if project.Metadata.UID == uid {
			raw.Matches = append(raw.Matches, selector.Match{Kind: metadata.KindProject, UID: uid, Name: project.Metadata.Name})
		}
	}
	return selector.Enforce(target, selector.DescribeSelector(query), raw)
}

func (s Service) Read(ctx context.Context, ref string) (Result, error) {
	return s.ReadWithOptions(ctx, ref, Options{})
}

// ReadWithOptions applies Registry admission options to this call only.
func (s Service) ReadWithOptions(ctx context.Context, ref string, options Options) (Result, error) {
	store, target, err := s.target(ctx, ref, options)
	if err != nil {
		return Result{}, err
	}
	rules, err := store.Load(target.UID)
	if err != nil {
		return Result{}, &Refusal{Code: CodeUnreadable, Reason: err.Error(), Cause: err}
	}
	return Project(target, rules), ctx.Err()
}

// Write validates before admission. Dry runs check the exact stored encoding.
// After admission the atomic store write completes; concurrent edits are not serialized.
func (s Service) Write(ctx context.Context, ref string, rules links.Rules, dryRun bool) (Result, error) {
	return s.WriteWithOptions(ctx, ref, rules, dryRun, Options{})
}

// WriteWithOptions applies Registry admission options to this call only.
func (s Service) WriteWithOptions(ctx context.Context, ref string, rules links.Rules, dryRun bool, options Options) (Result, error) {
	store, target, err := s.target(ctx, ref, options)
	if err != nil {
		return Result{}, err
	}
	if err := Check(rules); err != nil {
		return Result{}, validationRefusal(err)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !dryRun {
		if err := store.Write(target.UID, rules); err != nil {
			return Result{}, validationRefusal(err)
		}
	}
	out := Project(target, rules)
	out.DryRun = dryRun
	return out, nil
}
func (s Service) Delete(ctx context.Context, ref string, confirm bool) (Result, error) {
	return s.DeleteWithOptions(ctx, ref, confirm, Options{})
}

// DeleteWithOptions applies Registry admission options to this call only.
func (s Service) DeleteWithOptions(ctx context.Context, ref string, confirm bool, options Options) (Result, error) {
	store, target, err := s.target(ctx, ref, options)
	if err != nil {
		return Result{}, err
	}
	if !confirm {
		return Result{}, &Refusal{Code: CodeConfirmationRequired, Field: "confirm", Reason: "must be true; nothing was deleted"}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := store.Delete(target.UID); err != nil {
		return Result{}, err
	}
	return Project(target, links.Rules{}), nil
}

// Decode accepts the store's JSON format, bounded like a stored rule file.
func Decode(content []byte) (links.Rules, error) {
	if len(content) > links.MaxFileSize {
		return links.Rules{}, &Refusal{Code: CodeInvalid, Field: "links", Reason: fmt.Sprintf("input is %d bytes; the limit is %d", len(content), links.MaxFileSize)}
	}
	rules, err := links.Decode(content)
	if err != nil {
		return links.Rules{}, &Refusal{Code: CodeInvalid, Field: "rules", Reason: err.Error(), Cause: err}
	}
	return rules, nil
}
func validationRefusal(err error) error {
	var invalid *links.ValidationError
	if errors.As(err, &invalid) {
		return &Refusal{Code: CodeInvalid, Field: invalid.Field, Reason: invalid.Reason, Cause: err}
	}
	return err
}

// Check validates rules and bounds the exact normalized storage encoding.
func Check(rules links.Rules) error {
	if err := rules.Validate(); err != nil {
		return err
	}
	if rules.Jira == nil {
		rules.Jira = []string{}
	}
	if rules.Repo == nil {
		rules.Repo = []string{}
	}
	if rules.URLs == nil {
		rules.URLs = map[string]string{}
	}
	if rules.Links == nil {
		rules.Links = []links.Link{}
	}
	content, err := json.MarshalIndent(rules, "", "  ")
	if err != nil {
		return err
	}
	if size := len(content) + 1; size > links.MaxFileSize {
		return &links.ValidationError{Field: "links", Reason: fmt.Sprintf("the rules encode to %d bytes; the limit is %d", size, links.MaxFileSize)}
	}
	return nil
}

// Project builds examples and available placeholders from core, with sorted named URLs and labels.
func Project(target links.Project, rules links.Rules) Result {
	out := Result{Project: target.UID, Jira: append([]string{}, rules.Jira...), Repo: append([]string{}, rules.Repo...), URLs: make([]NamedURL, 0, len(rules.URLs)), Links: make([]Link, 0, len(rules.Links)), ExampleValue: ExampleValue, Placeholders: links.Placeholders(rules), ProjectVariables: []Variable{{links.PlaceholderProjectUID, target.UID}, {links.PlaceholderProjectName, target.Name}}}
	for _, name := range slices.Sorted(maps.Keys(rules.URLs)) {
		out.URLs = append(out.URLs, NamedURL{name, rules.URLs[name]})
	}
	for _, link := range rules.Links {
		item := Link{LabelKey: link.LabelKey, Template: link.Template}
		item.Example, _ = links.Resolve(rules, target, link.LabelKey, ExampleValue)
		out.Links = append(out.Links, item)
	}
	for _, key := range slices.Sorted(maps.Keys(target.Labels)) {
		out.ProjectVariables = append(out.ProjectVariables, Variable{strings.Replace(links.PlaceholderProjectLabel, "<key>", key, 1), target.Labels[key]})
	}
	return out
}
