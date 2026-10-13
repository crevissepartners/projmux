package diagnostics

import (
	"fmt"
	"regexp"
	"slices"
	"sync"
	"time"
)

const mutationLockSummaryEvent = "mutation.lock.summary"
const mutationLockWaitEvent = "mutation.lock.wait"

// MutationLockPolicy declares a caller's closed set of content-free site tokens.
// Its inventory is copied at construction and cannot be changed afterwards.
type MutationLockPolicy struct {
	sites   []string
	allowed map[string]struct{}
}

var mutationLockSiteToken = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)

func NewMutationLockPolicy(sites []string) (*MutationLockPolicy, error) {
	p := &MutationLockPolicy{sites: slices.Clone(sites), allowed: make(map[string]struct{}, len(sites))}
	for _, site := range sites {
		if len(site) > 96 || !mutationLockSiteToken.MatchString(site) {
			return nil, fmt.Errorf("invalid mutation lock site token")
		}
		if _, exists := p.allowed[site]; exists {
			return nil, fmt.Errorf("duplicate mutation lock site token")
		}
		p.allowed[site] = struct{}{}
	}
	return p, nil
}

func (p *MutationLockPolicy) permits(site string) bool {
	if p == nil {
		return false
	}
	_, ok := p.allowed[site]
	return ok
}

type mutationLockStats struct {
	count                                  int64
	waitTotal, waitMax, heldTotal, heldMax time.Duration
	lastSlow                               time.Time
}

// MutationLockRecorder aggregates completed acquisitions in memory. The first
// completion after each ten-minute interval emits one summary per active site;
// idle recorders write nothing. Slow waits emit at most once per site per minute.
// Append failures are best-effort, and never affect the protected operation.
type MutationLockRecorder struct {
	mu             sync.Mutex
	writer         EventWriter
	policy         *MutationLockPolicy
	runID, version string
	now            func() time.Time
	since          time.Time
	sites          map[string]*mutationLockStats
}

func NewMutationLockRecorder(writer EventWriter, policy *MutationLockPolicy, runID, version string, now func() time.Time) *MutationLockRecorder {
	if now == nil {
		now = time.Now
	}
	return &MutationLockRecorder{writer: writer, policy: policy, runID: runID, version: version, now: now, since: now(), sites: make(map[string]*mutationLockStats)}
}

func (r *MutationLockRecorder) Record(site string, wait, held time.Duration) {
	if r == nil || !r.policy.permits(site) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if wait < 0 {
		wait = 0
	}
	if held < 0 {
		held = 0
	}
	s := r.sites[site]
	if s == nil {
		s = &mutationLockStats{}
		r.sites[site] = s
	}
	s.count++
	s.waitTotal += wait
	s.heldTotal += held
	s.waitMax = max(s.waitMax, wait)
	s.heldMax = max(s.heldMax, held)
	if wait > 250*time.Millisecond && (s.lastSlow.IsZero() || now.Sub(s.lastSlow) >= time.Minute) {
		s.lastSlow = now
		r.emit(now, mutationLockWaitEvent, site, "mutation.lock.wait.slow", fmt.Sprintf("wait_ms=%d held_ms=%d", wait.Milliseconds(), held.Milliseconds()))
	}
	if now.Sub(r.since) < 10*time.Minute {
		return
	}
	for _, site := range r.policy.sites {
		s := r.sites[site]
		if s == nil || s.count == 0 {
			continue
		}
		r.emit(now, mutationLockSummaryEvent, site, "mutation.lock.summary", fmt.Sprintf("count=%d wait_total_ms=%d wait_max_ms=%d held_total_ms=%d held_max_ms=%d", s.count, s.waitTotal.Milliseconds(), s.waitMax.Milliseconds(), s.heldTotal.Milliseconds(), s.heldMax.Milliseconds()))
		*s = mutationLockStats{lastSlow: s.lastSlow}
	}
	r.since = now
}

func (r *MutationLockRecorder) emit(now time.Time, event, site, code, message string) {
	if r.writer != nil {
		_ = r.writer.Append(Event{At: now.UTC().Format(time.RFC3339Nano), Level: "info", Component: "mutation", Event: event, Result: "success", RunID: r.runID, Version: r.version, MuxBackend: MuxBackend(), Operation: site, Code: code, Message: message})
	}
}

var mutationLockSummaryMessage = regexp.MustCompile(`^count=[1-9][0-9]* wait_total_ms=[0-9]+ wait_max_ms=[0-9]+ held_total_ms=[0-9]+ held_max_ms=[0-9]+$`)
var mutationLockWaitMessage = regexp.MustCompile(`^wait_ms=[0-9]+ held_ms=[0-9]+$`)

func validateMutationLockEvent(event Event, policy *MutationLockPolicy) error {
	// Numeric messages must survive the shared sanitizer without truncation.
	if len(event.Message) > maxMessageRunes {
		return fmt.Errorf("mutation lock message too long")
	}
	if event.Level != "info" || event.Component != "mutation" || event.Result != "success" || !policy.permits(event.Operation) {
		return fmt.Errorf("invalid mutation lock event")
	}
	switch event.Event {
	case mutationLockSummaryEvent:
		if event.Code != "mutation.lock.summary" || !mutationLockSummaryMessage.MatchString(event.Message) {
			return fmt.Errorf("invalid mutation lock summary")
		}
	case mutationLockWaitEvent:
		if event.Code != "mutation.lock.wait.slow" || !mutationLockWaitMessage.MatchString(event.Message) {
			return fmt.Errorf("invalid mutation lock wait")
		}
	default:
		return fmt.Errorf("invalid mutation lock event name")
	}
	// A closed shape: no identities, arguments, paths or other family fields.
	event.At, event.Level, event.Component, event.Event, event.Result = "", "", "", "", ""
	event.RunID, event.Version, event.MuxBackend, event.Operation, event.Code, event.Message = "", "", "", "", "", ""
	if event != (Event{}) {
		return fmt.Errorf("unrelated fields on mutation lock event")
	}
	return nil
}
