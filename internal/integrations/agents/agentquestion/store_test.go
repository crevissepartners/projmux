package agentquestion

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

var storeTestEpoch = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// testClock is a settable store clock.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestStore(t *testing.T) (*Store, *testClock) {
	t.Helper()
	clock := &testClock{now: storeTestEpoch}
	return NewStore(t.TempDir()).WithClock(clock.Now), clock
}

func testID(n int) string { return fmt.Sprintf("question-%016x", n) }

func createTestRecord(t *testing.T, store *Store, clock *testClock, n int, agentUID string) Record {
	t.Helper()
	record, err := store.Create(Record{
		ID: testID(n), AgentUID: agentUID, PaneUID: "pan-" + agentUID, SessionID: "session-1", ToolUseID: "toolu_1",
		Questions: json.RawMessage(testQuestionsJSON), Deadline: clock.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

var testAnswers = map[string]string{"Which build tool?": "make, task", "Which branch?": "main"}

func readStoreBytes(t *testing.T, store *Store) []byte {
	t.Helper()
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestStoreCreateGetAndListRoundTrip(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	first := createTestRecord(t, store, clock, 1, "agt-a")
	clock.Advance(time.Second)
	createTestRecord(t, store, clock, 2, "agt-b")
	clock.Advance(time.Second)
	third := createTestRecord(t, store, clock, 3, "agt-a")

	got, found, err := store.Get(first.ID)
	if err != nil || !found || got.State != StateWaiting || got.AgentUID != "agt-a" || !got.Deadline.Equal(storeTestEpoch.Add(5*time.Minute)) {
		t.Fatalf("Get = %#v, %t, %v", got, found, err)
	}
	if _, err := got.ParsedQuestions(); err != nil {
		t.Fatalf("stored questions do not parse: %v", err)
	}
	list, err := store.List("agt-a")
	if err != nil || len(list) != 2 || list[0].ID != first.ID || list[1].ID != third.ID {
		t.Fatalf("List = %#v, %v", list, err)
	}
	if _, found, _ := store.Get(testID(9)); found {
		t.Fatal("unknown id found")
	}
	if _, err := store.Create(Record{ID: first.ID, AgentUID: "agt-a", Questions: json.RawMessage(testQuestionsJSON), Deadline: clock.Now().Add(time.Minute)}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("duplicate id err = %v", err)
	}
	if _, err := store.Create(Record{ID: "bogus", AgentUID: "agt-a", Questions: json.RawMessage(testQuestionsJSON), Deadline: clock.Now().Add(time.Minute)}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("bogus id err = %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store file mode = %v, %v", info, err)
	}
}

func TestStoreAnswerSettlesAWaitingRecordExactlyOnce(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a")
	clock.Advance(10 * time.Second)
	answered, err := store.Answer(record.ID, "agt-a", testAnswers)
	if err != nil || answered.State != StateAnswered || answered.Answers["Which build tool?"] != "make, task" || !answered.UpdatedAt.Equal(clock.Now()) {
		t.Fatalf("Answer = %#v, %v", answered, err)
	}
	before := readStoreBytes(t, store)
	if _, err := store.Answer(record.ID, "agt-a", testAnswers); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second answer err = %v, want ErrNotPending", err)
	}
	if !bytes.Equal(before, readStoreBytes(t, store)) {
		t.Fatal("refused answer changed the store")
	}
	settled, err := store.Settle(record.ID)
	if err != nil || settled.State != StateAnswered {
		t.Fatalf("Settle after answer = %#v, %v", settled, err)
	}
	closed, err := store.Close(record.ID, CloseReasonPopupFailed)
	if err != nil || closed.State != StateAnswered {
		t.Fatalf("Close after answer = %#v, %v", closed, err)
	}
}

func TestStoreAnswerRefusalsLeaveTheRecordUnchanged(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a")
	before := readStoreBytes(t, store)
	for name, test := range map[string]struct {
		id, agent string
		answers   map[string]string
		want      error
	}{
		"another Agent's question": {record.ID, "agt-b", testAnswers, ErrNotFound},
		"unknown question":         {testID(7), "agt-a", testAnswers, ErrNotFound},
		"wrong question key":       {record.ID, "agt-a", map[string]string{"Which build tool": "make", "Which branch?": "main"}, ErrInvalidAnswer},
		"missing answer":           {record.ID, "agt-a", map[string]string{"Which branch?": "main"}, ErrInvalidAnswer},
		"empty answer":             {record.ID, "agt-a", map[string]string{"Which build tool?": " ", "Which branch?": "main"}, ErrInvalidAnswer},
	} {
		if _, err := store.Answer(test.id, test.agent, test.answers); !errors.Is(err, test.want) {
			t.Errorf("%s: err = %v, want %v", name, err, test.want)
		}
	}
	if !bytes.Equal(before, readStoreBytes(t, store)) {
		t.Fatal("refused answers changed the store")
	}
}

func TestStoreWaitingRecordPastItsDeadlineReadsExpiredAndRefusesAnswers(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a")
	clock.Advance(5 * time.Minute)
	got, _, err := store.Get(record.ID)
	if err != nil || got.State != StateExpired || !got.UpdatedAt.Equal(record.Deadline) {
		t.Fatalf("Get at deadline = %#v, %v", got, err)
	}
	if list, _ := store.List("agt-a"); len(list) != 1 || list[0].State != StateExpired {
		t.Fatalf("List at deadline = %#v", list)
	}
	if _, err := store.Answer(record.ID, "agt-a", testAnswers); !errors.Is(err, ErrExpired) {
		t.Fatalf("answer at deadline err = %v, want ErrExpired", err)
	}
}

func TestStoreSettleExpiresOnlyAtTheDeadline(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a")
	clock.Advance(5*time.Minute - time.Millisecond)
	early, err := store.Settle(record.ID)
	if err != nil || early.State != StateWaiting {
		t.Fatalf("Settle before deadline = %#v, %v", early, err)
	}
	clock.Advance(time.Millisecond)
	settled, err := store.Settle(record.ID)
	if err != nil || settled.State != StateExpired {
		t.Fatalf("Settle at deadline = %#v, %v", settled, err)
	}
	// The expiry is on disk now, not only computed by readers.
	var state diskState
	if err := json.Unmarshal(readStoreBytes(t, store), &state); err != nil || state.Records[0].State != StateExpired {
		t.Fatalf("stored state = %#v, %v", state, err)
	}
	if _, err := store.Settle(testID(5)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Settle unknown err = %v", err)
	}
}

func TestStoreCloseEndsAWaitingRecordAndLaterAnswersAreRefused(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a")
	closed, err := store.Close(record.ID, CloseReasonHookCanceled)
	if err != nil || closed.State != StateClosed || closed.Disposition != "hook-canceled" {
		t.Fatalf("Close = %#v, %v", closed, err)
	}
	if _, err := store.Answer(record.ID, "agt-a", testAnswers); !errors.Is(err, ErrClosed) {
		t.Fatalf("answer after close err = %v, want ErrClosed", err)
	}
	late := createTestRecord(t, store, clock, 2, "agt-a")
	clock.Advance(6 * time.Minute)
	if got, err := store.Close(late.ID, CloseReasonPopupFailed); err != nil || got.State != StateExpired || got.Disposition != "" {
		t.Fatalf("Close past deadline = %#v, %v", got, err)
	}
}

func TestStoreCloseAgentClosesOnlyThatAgentsWaitingRecords(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	waiting := createTestRecord(t, store, clock, 1, "agt-a")
	answered := createTestRecord(t, store, clock, 2, "agt-a")
	other := createTestRecord(t, store, clock, 3, "agt-b")
	if _, err := store.Answer(answered.ID, "agt-a", testAnswers); err != nil {
		t.Fatal(err)
	}
	closed, err := store.CloseAgent("agt-a")
	if err != nil || closed != 1 {
		t.Fatalf("CloseAgent = %d, %v", closed, err)
	}
	for id, want := range map[string]State{waiting.ID: StateClosed, answered.ID: StateAnswered, other.ID: StateWaiting} {
		if got, _, _ := store.Get(id); got.State != want {
			t.Errorf("%s state = %s, want %s", id, got.State, want)
		}
	}
	if got, _, _ := store.Get(waiting.ID); got.Disposition != string(CloseReasonChannelOff) {
		t.Errorf("closed record disposition = %q, want %q", got.Disposition, CloseReasonChannelOff)
	}
	if got, _, _ := store.Get(answered.ID); got.Disposition != "" {
		t.Errorf("answered record disposition = %q, want none", got.Disposition)
	}
	before := readStoreBytes(t, store)
	if closed, err := store.CloseAgent("agt-a"); err != nil || closed != 0 || !bytes.Equal(before, readStoreBytes(t, store)) {
		t.Fatalf("repeat CloseAgent = %d, %v, unchanged=%t", closed, err, bytes.Equal(before, readStoreBytes(t, store)))
	}
}

// TestStoreCloseRecordsEachReasonAndSaysWhetherTheProviderStillAsks pins the
// reason vocabulary: every reason is a well-formed disposition, Close stores
// it as written, and only a canceled hook, an ended Codex turn, and an answer
// in Codex's own surface leave nothing asking.
func TestStoreCloseRecordsEachReasonAndSaysWhetherTheProviderStillAsks(t *testing.T) {
	t.Parallel()

	stillAsks := map[CloseReason]bool{
		CloseReasonAnsweredElsewhere: false,
		CloseReasonChannelOff:        true,
		CloseReasonHookCanceled:      false,
		CloseReasonHookFailed:        true,
		CloseReasonPopupDismissed:    true,
		CloseReasonPopupFailed:       true,
		CloseReasonTurnEnded:         false,
		CloseReasonWatchStopped:      true,
	}
	if len(closeReasonsStillAsked) != len(stillAsks) {
		t.Fatalf("the store writes %d reasons, want the %d of this test", len(closeReasonsStillAsked), len(stillAsks))
	}
	store, clock := newTestStore(t)
	for i, reason := range slices.Sorted(maps.Keys(closeReasonsStillAsked)) {
		want, ok := stillAsks[reason]
		if !ok {
			t.Fatalf("the store writes %q, which this test does not pin", reason)
		}
		if !dispositionPattern.MatchString(string(reason)) {
			t.Errorf("%q is not a well-formed disposition", reason)
		}
		if asks, known := ProviderStillAsks(string(reason)); !known || asks != want {
			t.Errorf("ProviderStillAsks(%q) = %t, %t, want %t, true", reason, asks, known, want)
		}
		if reason == CloseReasonAnsweredElsewhere {
			continue // Codex only, written by CloseAnsweredElsewhere.
		}
		record := createTestRecord(t, store, clock, i+1, "agt-a")
		closed, err := store.Close(record.ID, reason)
		if err != nil || closed.State != StateClosed || closed.Disposition != string(reason) {
			t.Fatalf("Close(%q) = %s/%q, %v", reason, closed.State, closed.Disposition, err)
		}
		if got, _, err := store.Get(record.ID); err != nil || got.Disposition != string(reason) {
			t.Fatalf("Get after Close(%q) = %q, %v", reason, got.Disposition, err)
		}
	}
	for _, disposition := range []string{"", "reason-no-release-writes-yet"} {
		if asks, known := ProviderStillAsks(disposition); asks || known {
			t.Errorf("ProviderStillAsks(%q) = %t, %t, want false, false", disposition, asks, known)
		}
	}
}

// TestStoreCloseRefusesAReasonNoReaderTakes pins that Close writes nothing for
// a reason outside the disposition form, or for answered-elsewhere on a Claude
// record, so the store stays readable and the record keeps waiting.
func TestStoreCloseRefusesAReasonNoReaderTakes(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a")
	before := readStoreBytes(t, store)
	for _, reason := range []CloseReason{"", "Popup-Failed", "popup failed", CloseReason(strings.Repeat("a", 41)), CloseReasonAnsweredElsewhere} {
		if got, err := store.Close(record.ID, reason); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("Close(%q) = %s/%q, %v, want ErrInvalidRecord", reason, got.State, got.Disposition, err)
		}
	}
	if !bytes.Equal(before, readStoreBytes(t, store)) {
		t.Fatal("a refused Close changed the store")
	}
	if got, _, err := store.Get(record.ID); err != nil || got.State != StateWaiting {
		t.Fatalf("record after refused closes = %s, %v, want waiting", got.State, err)
	}
	if got, err := store.Close(record.ID, CloseReasonPopupFailed); err != nil || got.Disposition != string(CloseReasonPopupFailed) {
		t.Fatalf("valid Close after refusals = %s/%q, %v", got.State, got.Disposition, err)
	}
}

func TestStorePrunesSettledRecordsAndNeverEvictsWaitingOnes(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	old := createTestRecord(t, store, clock, 1, "agt-a")
	if _, err := store.Close(old.ID, CloseReasonPopupFailed); err != nil {
		t.Fatal(err)
	}
	clock.Advance(terminalRetention + time.Second)
	createTestRecord(t, store, clock, 2, "agt-a")
	if _, found, _ := store.Get(old.ID); found {
		t.Fatal("a record settled longer than the retention ago survived a write")
	}

	store, clock = newTestStore(t)
	for n := range maxRecords {
		createTestRecord(t, store, clock, n+1, "agt-a")
	}
	if _, err := store.Create(Record{ID: testID(maxRecords + 1), AgentUID: "agt-a", Questions: json.RawMessage(testQuestionsJSON), Deadline: clock.Now().Add(time.Minute)}); !errors.Is(err, ErrCapacity) {
		t.Fatalf("create in a store full of waiting records err = %v, want ErrCapacity", err)
	}
	if _, err := store.Close(testID(7), CloseReasonPopupFailed); err != nil {
		t.Fatal(err)
	}
	createTestRecord(t, store, clock, maxRecords+1, "agt-a")
	if _, found, _ := store.Get(testID(7)); found {
		t.Fatal("the settled record was not the one evicted to make room")
	}
	if list, _ := store.List("agt-a"); len(list) != maxRecords {
		t.Fatalf("records = %d, want %d", len(list), maxRecords)
	}
}

func TestStoreRejectsAMalformedFile(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"garbage":       "not json",
		"wrong version": `{"version":9,"records":[]}`,
		"unknown field": `{"version":1,"records":[],"extra":true}`,
		"bad record":    `{"version":1,"records":[{"id":"question-0000000000000001","agentUID":"a","questions":[],"createdAt":"2026-09-19T12:00:00Z","deadline":"2026-09-19T12:05:00Z","state":"waiting","updatedAt":"2026-09-19T12:00:00Z"}]}`,
	} {
		if err := os.WriteFile(store.Path(), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Get(testID(1)); !errors.Is(err, ErrMalformedStore) {
			t.Errorf("%s: Get err = %v, want ErrMalformedStore", name, err)
		}
	}
}

func TestStoreConcurrentAnswersSettleTheRecordOnce(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	record := createTestRecord(t, store, clock, 1, "agt-a")
	const writers = 8
	var wg sync.WaitGroup
	results := make(chan error, writers)
	for range writers {
		wg.Go(func() {
			// Each writer opens its own view of the same file, as separate
			// processes would.
			_, err := NewStoreAt(store.Path()).WithClock(clock.Now).Answer(record.ID, "agt-a", testAnswers)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	won, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrNotPending):
			refused++
		default:
			t.Errorf("unexpected err = %v", err)
		}
	}
	if won != 1 || refused != writers-1 {
		t.Fatalf("won=%d refused=%d, want 1 and %d", won, refused, writers-1)
	}
}

// seedRecord is a record as some release wrote it, written straight to disk.
func seedRecord(n int, agentUID string, state State, disposition string, now time.Time) Record {
	record := Record{
		ID: testID(n), AgentUID: agentUID, Questions: json.RawMessage(testQuestionsJSON),
		CreatedAt: now, Deadline: now.Add(5 * time.Minute), State: state, Disposition: disposition, UpdatedAt: now,
	}
	if state == StateAnswered {
		record.Answers = testAnswers
	}
	return record
}

func writeSeededStore(t *testing.T, store *Store, records ...Record) {
	t.Helper()
	data, err := json.Marshal(diskState{Version: storeVersion, Records: records})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreReadsClosedRecordsWithAnyWellFormedReasonAndKeepsThem(t *testing.T) {
	t.Parallel()

	store, clock := newTestStore(t)
	now := clock.Now()
	longest := "a" + strings.Repeat("0-", 19) + "z"
	codex := seedRecord(8, "agt-c", StateClosed, "answered-elsewhere", now)
	codex.Provider = "codex"
	codex.RequestID = "request-1"
	codex.Questions = json.RawMessage(`[{"id":"q","question":"Pick","options":[{"label":"A"}]}]`)
	expiring := seedRecord(5, "agt-a", StateWaiting, "", now)
	expiring.Deadline = now.Add(-time.Second)
	writeSeededStore(t, store,
		seedRecord(1, "agt-a", StateClosed, "popup-dismissed", now),
		seedRecord(2, "agt-a", StateClosed, "reason-no-release-writes-yet", now),
		seedRecord(3, "agt-a", StateWaiting, "", now),
		seedRecord(4, "agt-a", StateWaiting, "", now),
		expiring,
		seedRecord(6, "agt-b", StateWaiting, "", now),
		seedRecord(7, "agt-b", StateClosed, longest, now),
		codex,
	)
	reasons := map[string]string{testID(1): "popup-dismissed", testID(2): "reason-no-release-writes-yet", testID(7): longest, testID(8): "answered-elsewhere"}
	keepsReasons := func(step string) {
		t.Helper()
		for id, want := range reasons {
			got, found, err := store.Get(id)
			if err != nil || !found || got.State != StateClosed || got.Disposition != want || !got.UpdatedAt.Equal(now) {
				t.Fatalf("after %s: Get(%s) = %s/%q at %s, found=%t, err=%v", step, id, got.State, got.Disposition, got.UpdatedAt, found, err)
			}
		}
	}

	keepsReasons("seeding")
	list, err := store.List("agt-a")
	if err != nil || len(list) != 5 {
		t.Fatalf("List = %d records, %v", len(list), err)
	}
	clock.Advance(time.Second)
	if _, err := store.Answer(testID(3), "agt-a", testAnswers); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	keepsReasons("Answer")
	if got, err := store.Close(testID(4), CloseReasonPopupDismissed); err != nil || got.State != StateClosed || got.Disposition != "popup-dismissed" {
		t.Fatalf("Close = %s/%q, %v", got.State, got.Disposition, err)
	}
	keepsReasons("Close")
	if got, err := store.Settle(testID(5)); err != nil || got.State != StateExpired {
		t.Fatalf("Settle = %s, %v", got.State, err)
	}
	keepsReasons("Settle")
	if closed, err := store.CloseAgent("agt-b"); err != nil || closed != 1 {
		t.Fatalf("CloseAgent = %d, %v", closed, err)
	}
	keepsReasons("CloseAgent")
	createTestRecord(t, store, clock, 9, "agt-a")
	keepsReasons("Create")

	for id, want := range map[string]error{testID(1): ErrClosed, testID(2): ErrClosed, testID(8): ErrAnsweredElsewhere} {
		agentUID := "agt-a"
		if id == testID(8) {
			agentUID = "agt-c"
		}
		if _, err := store.Answer(id, agentUID, testAnswers); !errors.Is(err, want) {
			t.Errorf("Answer(%s) err = %v, want %v", id, err, want)
		}
	}
	keepsReasons("refused answers")
}

func TestStoreRejectsAReasonOutsideItsFormOrOnAnOpenRecord(t *testing.T) {
	t.Parallel()

	now := storeTestEpoch
	claudeElsewhere := seedRecord(1, "agt-a", StateClosed, "answered-elsewhere", now)
	for name, record := range map[string]Record{
		"upper case":                   seedRecord(1, "agt-a", StateClosed, "Popup-dismissed", now),
		"leading digit":                seedRecord(1, "agt-a", StateClosed, "1popup", now),
		"leading dash":                 seedRecord(1, "agt-a", StateClosed, "-popup", now),
		"underscore":                   seedRecord(1, "agt-a", StateClosed, "popup_dismissed", now),
		"space":                        seedRecord(1, "agt-a", StateClosed, "popup dismissed", now),
		"forty-one bytes":              seedRecord(1, "agt-a", StateClosed, "a"+strings.Repeat("b", 40), now),
		"waiting with a reason":        seedRecord(1, "agt-a", StateWaiting, "popup-dismissed", now),
		"answered with a reason":       seedRecord(1, "agt-a", StateAnswered, "popup-dismissed", now),
		"expired with a reason":        seedRecord(1, "agt-a", StateExpired, "popup-dismissed", now),
		"answered-elsewhere on claude": claudeElsewhere,
	} {
		store, _ := newTestStore(t)
		writeSeededStore(t, store, record)
		if _, _, err := store.Get(testID(1)); !errors.Is(err, ErrMalformedStore) {
			t.Errorf("%s: Get err = %v, want ErrMalformedStore", name, err)
		}
	}
}

// TestCloseReasonDocsTableMatchesTheStore holds docs/hooks.md's close reason
// table to the reasons the store writes and to whether each leaves the
// provider asking.
func TestCloseReasonDocsTableMatchesTheStore(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "hooks.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, table, found := strings.Cut(string(raw), "| Disposition | Written when | Provider still asks |\n")
	if !found {
		t.Fatal("docs/hooks.md has no close reason table")
	}
	row := regexp.MustCompile("^\\| `([a-z][a-z0-9-]*)` \\| .+ \\| (yes|no) \\|$")
	documented := map[CloseReason]bool{}
	for _, line := range strings.Split(table, "\n")[1:] {
		if !strings.HasPrefix(line, "|") {
			break
		}
		match := row.FindStringSubmatch(line)
		if match == nil {
			t.Fatalf("close reason row %q is not `reason` | when | yes/no", line)
		}
		documented[CloseReason(match[1])] = match[2] == "yes"
	}
	if !maps.Equal(documented, closeReasonsStillAsked) {
		t.Fatalf("docs close reasons = %v, store = %v", documented, closeReasonsStillAsked)
	}
}
