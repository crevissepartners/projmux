package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// pruneProjectCandidate is one Project the missing-root prune would remove.
type pruneProjectCandidate struct {
	UID       string
	Name      string
	Root      string
	MissingAt time.Time
}

// pruneProjectCommand implements the canonical `prune project` route.
//
// The route is deliberately narrow and explicit. It only ever considers
// Projects whose `spec.root` is recorded as missing, it refuses to run without
// both `--missing` and `--older-than`, it lists a bounded set of candidates by
// default, and it deletes nothing at all until `--yes` is passed. A Project with
// a live runtime projection and a Project whose root has come back are both
// excluded, so a recovered Project can never be pruned by age alone.
type pruneProjectCommand struct {
	store *resourceStore
	now   func() time.Time
}

func newPruneProjectCommand() *pruneProjectCommand {
	return &pruneProjectCommand{store: newResourceStore(), now: time.Now}
}

func (c *pruneProjectCommand) Run(args []string, stdout, stderr io.Writer) error {
	const spelling = "prune project"

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	missing := fs.Bool("missing", false, "select Projects whose spec.root has disappeared")
	olderThan := fs.String("older-than", "", "minimum age of the MissingRoot observation, for example 720h")
	yes := fs.Bool("yes", false, "actually delete the listed Projects")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("%s does not accept positional arguments; got %q", spelling, fs.Arg(0)))
	}
	// Both selectors are mandatory. A bare `prune project` must never be able to
	// mean "every Project": the destructive scope has to be spelled out.
	if !*missing {
		return usageError(spelling + " requires --missing; it prunes only Projects whose spec.root has disappeared")
	}
	if strings.TrimSpace(*olderThan) == "" {
		return usageError(spelling + " requires --older-than <duration>, for example --older-than 720h")
	}
	age, err := time.ParseDuration(*olderThan)
	if err != nil {
		return usageError(fmt.Sprintf("%s --older-than %q is not a duration: %v", spelling, *olderThan, err))
	}
	if age < 0 {
		return usageError(spelling + " --older-than must not be negative")
	}

	registry, err := c.store.load()
	if err != nil {
		return MapMetadataError(err)
	}
	if len(registry.Projects) == 0 {
		_, err := fmt.Fprintf(stdout, "%s: no Projects are registered\n", spelling)
		return err
	}

	// Classification runs against a private copy, so the default listing never
	// writes the refreshed MissingRoot observations back to disk.
	preview := registry.Clone()
	mutator := c.store.mutator()
	if err := mutator.ObserveProjectRoots(&preview); err != nil {
		return MapMetadataError(err)
	}
	candidates := pruneProjectCandidates(preview, c.now().UTC(), age)

	if !*yes {
		return writePruneProjectPlan(stdout, spelling, candidates, true)
	}
	if len(candidates) == 0 {
		return writePruneProjectPlan(stdout, spelling, candidates, false)
	}

	uids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		uids = append(uids, candidate.UID)
	}
	if err := c.store.mutate(coremetadata.KindProject, uids,
		func(working *coremetadata.Registry, mutator coremetadata.Mutator) error {
			// Re-observe and re-select inside the lock: a root that came back
			// between the listing and the confirmation drops out of the set here
			// rather than being deleted on stale evidence.
			if err := mutator.ObserveProjectRoots(working); err != nil {
				return err
			}
			approved := map[string]bool{}
			for _, candidate := range pruneProjectCandidates(*working, c.now().UTC(), age) {
				approved[candidate.UID] = true
			}
			for _, uid := range uids {
				if !approved[uid] {
					return fmt.Errorf("%s: project %q no longer matches --missing --older-than; nothing was deleted", spelling, uid)
				}
			}
			for _, uid := range uids {
				if err := mutator.DeleteProject(working, uid); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
		return err
	}
	return writePruneProjectPlan(stdout, spelling, candidates, false)
}

// pruneProjectCandidates selects the Projects eligible for a missing-root prune.
func pruneProjectCandidates(registry coremetadata.Registry, now time.Time, age time.Duration) []pruneProjectCandidate {
	var out []pruneProjectCandidate
	for _, project := range registry.Projects {
		condition, ok := project.HasCondition(coremetadata.ConditionMissingRoot)
		if !ok || condition.Status != coremetadata.ConditionTrue {
			// The root exists again. A recovered Project keeps its uid and is
			// never pruned by age.
			continue
		}
		if session := project.Status.Session; session != nil && session.Live {
			// A live runtime is proof the Project is still in use.
			continue
		}
		if now.Sub(condition.FirstObservedAt.UTC()) < age {
			continue
		}
		out = append(out, pruneProjectCandidate{
			UID:       project.Metadata.UID,
			Name:      project.Metadata.Name,
			Root:      project.Spec.Root,
			MissingAt: condition.FirstObservedAt.UTC(),
		})
	}
	return out
}

// writePruneProjectPlan renders the bounded candidate listing.
//
// The listing is capped at the same bound the selector contract uses for
// ambiguity output, so a wide match reports a count instead of flooding a
// terminal or a log.
func writePruneProjectPlan(stdout io.Writer, spelling string, candidates []pruneProjectCandidate, dryRun bool) error {
	var b strings.Builder
	if len(candidates) == 0 {
		fmt.Fprintf(&b, "%s: no Project matches --missing --older-than\n", spelling)
		_, err := io.WriteString(stdout, b.String())
		return err
	}
	verb := "deleted"
	if dryRun {
		verb = "would delete"
	}
	fmt.Fprintf(&b, "%s: %s %d project%s\n", spelling, verb, len(candidates), plural(len(candidates)))
	shown := candidates
	omitted := 0
	if len(shown) > selector.MaxCandidates {
		omitted = len(shown) - selector.MaxCandidates
		shown = shown[:selector.MaxCandidates]
	}
	for _, candidate := range shown {
		fmt.Fprintf(&b, "  project/%s uid=%s root=%s missingSince=%s\n",
			candidate.Name, candidate.UID, candidate.Root, candidate.MissingAt.Format("2006-01-02T15:04:05Z"))
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "  ... %d more omitted\n", omitted)
	}
	if dryRun {
		b.WriteString("dry-run: nothing was deleted; re-run with --yes to delete\n")
	}
	_, err := io.WriteString(stdout, b.String())
	return err
}
