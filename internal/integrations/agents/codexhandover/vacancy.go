package codexhandover

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

// stateDomainThreadsDir is the shared thread store inside one Codex state
// domain. Generations own a private endpoint root, never a private thread
// store, so this directory is what every generation in the domain shares.
const stateDomainThreadsDir = "sessions"

// rolloutThreadID matches the trailing thread identity of a rollout file name.
var rolloutThreadID = regexp.MustCompile(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

// enumerateStateDomainThreads lists the thread ids the shared state domain
// holds. It reads file names only: no rollout body is opened, so a census
// cannot see prompt, transcript, or credential material.
//
// The walk does not descend through symlinked directories. That keeps it
// bounded without a cycle guard, and the blind spot is safe in one direction
// only -- a missed thread can never turn a bound generation into a vacant one,
// because a binding is proven from the Registry side and the store read only
// confirms it.
func enumerateStateDomainThreads(stateDomainPath string) ([]string, error) {
	root := filepath.Join(stateDomainPath, stateDomainThreadsDir)
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, &fs.PathError{Op: "readdir", Path: root, Err: fs.ErrInvalid}
	}
	var threads []string
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// One unreadable subtree must not be read as an empty one.
			return err
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		body := strings.TrimSuffix(name, ".jsonl")
		if match := rolloutThreadID.FindStringSubmatch(body); match != nil {
			threads = append(threads, match[1])
			return nil
		}
		threads = append(threads, strings.TrimPrefix(body, "rollout-"))
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	slices.Sort(threads)
	return slices.Compact(threads), nil
}

// stateDomainPath returns the shared state domain root recorded by any managed
// route in the journal. An unmanaged route carries no configuration at all, so
// the retiring generation itself usually cannot supply it; every managed route
// in one journal names the same domain, which is why any of them will do.
func stateDomainPath(journal codexupgrade.Journal, preferred metadata.CodexEndpointRef) string {
	if route, ok := journal.Route(preferred); ok && route.Config.StateDomainPath != "" {
		return route.Config.StateDomainPath
	}
	for _, route := range journal.Routes {
		if route.Config.StateDomainPath != "" {
			return route.Config.StateDomainPath
		}
	}
	return ""
}

// gatherRetirementVacancy censuses one exact retiring endpoint and returns both
// the evidence and its verdict.
//
// The obligation half is projected from the caller's Registry snapshot, never
// read from the journal: a journal obligation whose Agent has since been
// deleted is frozen at its last projection and can neither be recomputed nor
// traced, which is exactly the stale input this decision must not consume.
//
// The store half is the second angle. Obligations carry no ThreadID, so the
// census also collects every thread the Registry still binds to this endpoint
// and confirms against the shared state domain whether those threads exist.
// A domain that cannot be read yields no verdict of vacancy.
func gatherRetirementVacancy(
	registry metadata.Registry,
	endpoint metadata.CodexEndpointRef,
	domainPath string,
	enumerate func(string) ([]string, error),
) (codexgeneration.RetirementVacancyEvidence, codexgeneration.RetirementVacancy) {
	evidence := codexgeneration.RetirementVacancyEvidence{ObligationsProjected: true}
	claimed := map[string]struct{}{}
	for i := range registry.Agents {
		agent := registry.Agents[i]
		ref := agent.Status.SessionRef
		if ref == nil || ref.Codex == nil || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Same(endpoint) {
			continue
		}
		evidence.EndpointBoundAgents++
		if thread := strings.TrimSpace(ref.Codex.ThreadID); thread != "" {
			claimed[thread] = struct{}{}
		}
		if obligation, ok := codexgeneration.ProjectAgentObligation(agent, false); ok &&
			obligation.State != codexgeneration.ObligationClosed {
			evidence.LiveObligations++
		}
	}
	for i := range registry.Panes {
		binding := registry.Panes[i].Status.Activation.Codex
		if binding == nil || binding.Authority == nil {
			continue
		}
		authority := metadata.CodexEndpointRef{
			StateDomainID:        binding.Authority.StateDomainID,
			EndpointGenerationID: binding.Authority.EndpointGenerationID,
		}
		if !authority.Same(endpoint) {
			continue
		}
		evidence.EndpointBoundPanes++
		if thread := strings.TrimSpace(binding.ThreadID); thread != "" {
			claimed[thread] = struct{}{}
		}
	}
	if domainPath != "" && enumerate != nil {
		if threads, err := enumerate(domainPath); err == nil {
			evidence.ThreadsEnumerated = true
			evidence.EnumeratedThreads = len(threads)
			for _, thread := range threads {
				if _, bound := claimed[thread]; bound {
					evidence.BoundThreadsPresent++
				}
			}
		}
	}
	return evidence, codexgeneration.EvaluateRetirementVacancy(evidence)
}
