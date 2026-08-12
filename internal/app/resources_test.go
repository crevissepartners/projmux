package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coreresources "github.com/crevissepartners/projmux/internal/core/resources"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestResourceViewHierarchyDisplayResolverSortAndExplicitRoots(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 2, 0, time.UTC)
	view := newResourceViewState(func() time.Time { return now }, i18n.FallbackLocale)
	view.setSnapshot(resourceReadySnapshot(now.Add(-time.Second)), false)

	items, header, footer := renderResourceView(view)
	api := pickerItemByValue(items, "project:/repo/api")
	if len(items) != 3 || api == nil || api.Label != "api" || !strings.Contains(strings.Join(api.MetaLines, "\n"), "Context  /repo/api") {
		t.Fatalf("project items = %#v, want primary identity separate from path context", items)
	}
	for _, value := range []string{"project:" + coreresources.ProjectUnassigned, "project:" + coreresources.ProjectShared} {
		if pickerItemByValue(items, value) == nil {
			t.Fatalf("project items = %#v, want explicit stable root %q", items, value)
		}
	}
	if !strings.Contains(header, "Host CPU 50.0%") || !strings.Contains(header, "Attributed CPU 12.0%") || !strings.Contains(header, "RSS") || !strings.Contains(header, "sample 1s") || !strings.Contains(header, "ready") || !strings.Contains(header, "Other / unattributed") || !strings.Contains(header, "not drillable") {
		t.Fatalf("header = %q, want separate host/attributed/Other bands", header)
	}
	if !strings.Contains(footer, "Tab: sort CPU") || strings.Contains(footer, " r:") {
		t.Fatalf("footer = %q, want Tab/Ctrl-R and no printable r shortcut", footer)
	}

	view.enter("project:/repo/api")
	items, _, _ = renderResourceView(view)
	if len(items) != 1 || items[0].Label != "editor @7" || !strings.Contains(strings.Join(items[0].MetaLines, "\n"), "Session dev") || !strings.Contains(items[0].SearchText, "@7") {
		t.Fatalf("window items = %#v, want display/session context and stable id", items)
	}
	view.enter(items[0].Value)
	items, _, _ = renderResourceView(view)
	if len(items) != 2 || !pickerItemsContain(items, "user label") || !pickerItemsContain(items, "agent topic") || !strings.Contains(items[0].SearchText+items[1].SearchText, "%11") {
		t.Fatalf("pane items = %#v, want canonical display identities plus pane ids", items)
	}
	view.cycleSort()
	items, _, _ = renderResourceView(view)
	if !strings.Contains(items[0].SearchText, "%11") {
		t.Fatalf("memory sort first = %#v, want larger RSS pane %%11", items[0])
	}
	view.cycleSort()
	items, _, _ = renderResourceView(view)
	if !strings.Contains(items[0].Label, "agent topic") {
		t.Fatalf("name sort first = %q, want resolved agent topic", items[0].Label)
	}
	view.enter(items[0].Value)
	items, _, footer = renderResourceView(view)
	if len(items) != 1 || items[0].Value != "" || items[0].Label != "Pane details" || !strings.Contains(strings.Join(items[0].MetaLines, "\n"), "CPU host share: 2.0% · 0.16c") || !strings.Contains(strings.Join(items[0].MetaLines, "\n"), "Memory RSS sum") || !strings.Contains(footer, "Read-only pane detail") {
		t.Fatalf("pane detail = %#v footer=%q", items, footer)
	}
	for i := range 3 {
		if !view.back() {
			t.Fatalf("back navigation stopped at step %d", i+1)
		}
	}
	if view.back() {
		t.Fatal("root back should close instead of returning to another scope")
	}
}

func TestResourceBreadcrumbActionableListEmptyAndReadOnlyDetailChrome(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 2, 0, time.UTC)
	view := newResourceViewState(func() time.Time { return now }, i18n.FallbackLocale)
	view.setSnapshot(resourceReadySnapshot(now), false)
	cmd := &resourceCommand{homeDir: func() (string, error) { return "", errors.New("no config") }, lookupEnv: func(string) string { return "" }}
	lifecycle := &resourceLifecycle{trigger: make(chan struct{})}

	root := cmd.pickerOptions(view, lifecycle)
	if root.Title != "Resources · Projects" || root.DisableSearch || root.ReadOnly || !pickerActionsContain(root.Actions, "tab") {
		t.Fatalf("root options = %#v, want one root breadcrumb and actionable list chrome", root)
	}
	if pickerItemsContain(root.Items, coreresources.OtherUnattributed) || !chromeBandsContain(root.ChromeBands, "Other / unattributed") {
		t.Fatalf("root items=%#v bands=%#v, want Other summary outside actionable rows", root.Items, root.ChromeBands)
	}
	if pickerItemByValue(root.Items, "project:"+coreresources.ProjectUnassigned) == nil || pickerItemByValue(root.Items, "project:"+coreresources.ProjectShared) == nil {
		t.Fatalf("root items=%#v, want explicit Unassigned/Shared drill-down roots", root.Items)
	}

	view.enter("project:/repo/api")
	windows := cmd.pickerOptions(view, lifecycle)
	if windows.Title != "Resources · Projects / api" {
		t.Fatalf("window breadcrumb = %q", windows.Title)
	}
	view.enter(windows.Items[0].Value)
	panes := cmd.pickerOptions(view, lifecycle)
	if panes.Title != "Resources · Projects / api / editor @7" {
		t.Fatalf("pane breadcrumb = %q", panes.Title)
	}
	view.enter(panes.Items[0].Value)
	detail := cmd.pickerOptions(view, lifecycle)
	if !strings.HasPrefix(detail.Title, "Resources · Projects / api / editor @7 / ") || !detail.DisableSearch || !detail.ReadOnly || detail.Prompt == "" || pickerActionsContain(detail.Actions, "tab") || strings.Contains(detail.Footer, "Enter") || strings.Contains(detail.Footer, "sort") || detail.Items[0].Value != "" {
		t.Fatalf("detail options = %#v, want read-only panel chrome without search/pointer/Enter/sort", detail)
	}

	view.setSnapshot(coreresources.Snapshot{At: now, Status: coreresources.StatusReady}, false)
	gone := cmd.pickerOptions(view, lifecycle)
	if !gone.ReadOnly || !gone.DisableSearch || gone.Items[0].Value != "" || !strings.Contains(gone.Items[0].Label, "no longer exists") {
		t.Fatalf("gone options = %#v, want explicit non-actionable vanished panel", gone)
	}

	view.back()
	empty := cmd.pickerOptions(view, lifecycle)
	if len(empty.Items) != 0 || !empty.ReadOnly || !empty.DisableSearch || strings.Contains(empty.Footer, "Enter") || !chromeBandsContain(empty.ChromeBands, "No panes") {
		t.Fatalf("empty options = %#v, want explicit read-only empty surface", empty)
	}
}

func TestResourceStructuredMetricColumnsStayAlignedAcrossKnownAndUnknownRows(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 2, 0, time.UTC)
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, i18n.Locale("ko-KR")} {
		view := newResourceViewState(func() time.Time { return now }, locale)
		snapshot := resourceReadySnapshot(now)
		snapshot.Projects = append(snapshot.Projects, coreresources.ProjectUsage{Key: coreresources.ProjectUnassigned})
		view.setSnapshot(snapshot, false)
		items := view.screen().items
		if len(items) != 3 {
			t.Fatalf("locale %s rows=%#v", locale, items)
		}
		var wantCPU, wantMemory, wantCount int
		for index, item := range items {
			metric := item.MetaLines[len(item.MetaLines)-1]
			cpu := strings.Index(metric, "CPU")
			memoryLabel := "MEMORY"
			countLabel := "PANES"
			if locale == i18n.Locale("ko-KR") {
				memoryLabel, countLabel = "메모리", "PANE"
			}
			memory := strings.Index(metric, memoryLabel)
			count := strings.Index(metric, countLabel)
			if index == 0 {
				wantCPU, wantMemory, wantCount = cpu, memory, count
				continue
			}
			if cpu != wantCPU || memory != wantMemory || count != wantCount {
				t.Fatalf("locale %s metric columns drift: first=(%d,%d,%d) row=(%d,%d,%d) metrics=%#v", locale, wantCPU, wantMemory, wantCount, cpu, memory, count, items)
			}
		}
	}
}

func TestResourceDeferredUpdateReconcilesActionableAndReadOnlyActions(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 2, 0, time.UTC)
	home := t.TempDir()
	path, err := keymapPath(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepathDir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[bindings.\"Resources:Open\"]\nkeys = [\"M-x\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ready := resourceReadySnapshot(now)
	empty := coreresources.Snapshot{At: now, Status: coreresources.StatusReady}
	collector := &sequenceResourceCollector{snapshots: []coreresources.Snapshot{empty, ready}}
	lifecycle := newResourceLifecycle(collector, func() time.Time { return now }, time.Hour)
	defer lifecycle.cancel()
	view := newResourceViewState(func() time.Time { return now }, i18n.FallbackLocale)
	view.setSnapshot(ready, false)
	view.enter("project:/repo/api")
	cmd := &resourceCommand{homeDir: func() (string, error) { return home, nil }, lookupEnv: func(string) string { return "" }}

	populated := cmd.pickerOptions(view, lifecycle)
	for _, key := range []string{"tab", "ctrl-r", "alt-left", "alt-x"} {
		if !pickerActionsContain(populated.Actions, key) {
			t.Fatalf("populated actions=%#v, missing %q", populated.Actions, key)
		}
	}
	toEmpty, err := populated.DeferredUpdate()
	if err != nil || !toEmpty.SetActions || pickerActionsContain(toEmpty.Actions, "tab") || !toEmpty.ReadOnly || !toEmpty.DisableSearch {
		t.Fatalf("populated→empty update=%#v err=%v, want Tab removed and read-only enabled", toEmpty, err)
	}
	for _, key := range []string{"ctrl-r", "alt-left", "alt-x"} {
		if !pickerActionsContain(toEmpty.Actions, key) {
			t.Fatalf("empty actions=%#v, missing preserved %q", toEmpty.Actions, key)
		}
	}

	emptyOptions := cmd.pickerOptions(view, lifecycle)
	toPopulated, err := emptyOptions.DeferredUpdate()
	if err != nil || !toPopulated.SetActions || !pickerActionsContain(toPopulated.Actions, "tab") || toPopulated.ReadOnly || toPopulated.DisableSearch {
		t.Fatalf("empty→populated update=%#v err=%v, want Tab restored and list interaction enabled", toPopulated, err)
	}
	for _, key := range []string{"ctrl-r", "alt-left", "alt-x"} {
		if !pickerActionsContain(toPopulated.Actions, key) {
			t.Fatalf("populated actions=%#v, missing preserved %q", toPopulated.Actions, key)
		}
	}
}

func TestResourceCommandAltLeftAndEscWalkParentThenCloseRoot(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 2, 0, time.UTC)
	collector := &sequenceResourceCollector{snapshots: []coreresources.Snapshot{resourceReadySnapshot(now)}}
	var titles []string
	call := 0
	cmd := &resourceCommand{
		collector: collector,
		picker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			titles = append(titles, options.Title)
			call++
			switch call {
			case 1:
				if _, err := options.DeferredUpdate(); err != nil {
					t.Fatalf("initial resource sample: %v", err)
				}
				return intpicker.Result{Key: "enter", Value: "project:/repo/api"}, nil
			case 2, 3:
				return intpicker.Result{Key: "enter", Value: options.Items[0].Value}, nil
			case 4:
				action, ok := pickerAction(options.Actions, "alt-left")
				if !ok || action.Mutate == nil {
					t.Fatalf("detail actions=%#v, want Alt-Left back action", options.Actions)
				}
				update, err := action.Mutate(intpicker.ActionContext{Key: "alt-left"})
				if err != nil || update.Result == nil {
					t.Fatalf("Alt-Left update=%#v err=%v", update, err)
				}
				return *update.Result, nil
			default:
				return intpicker.Result{Key: "esc", Closed: true}, nil
			}
		}),
		homeDir:   func() (string, error) { return "", errors.New("no config") },
		lookupEnv: func(string) string { return "" },
		now:       func() time.Time { return now },
		interval:  time.Hour,
	}
	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Resources · Projects",
		"Resources · Projects / api",
		"Resources · Projects / api / editor @7",
		"Resources · Projects / api / editor @7 / user label %10",
		"Resources · Projects / api / editor @7",
		"Resources · Projects / api",
		"Resources · Projects",
	}
	if !slices.Equal(titles, want) {
		t.Fatalf("back flow titles=%#v, want %#v", titles, want)
	}
}

func TestAppRunResourcesCLIAndDefaultCadence(t *testing.T) {
	t.Parallel()

	app := New()
	var stdout, stderr bytes.Buffer
	if err := app.Run([]string{"resources", "--help"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Usage: projmux resources") {
		t.Fatalf("resources help = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := app.Run([]string{"resources", "extra"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "does not accept positional arguments") {
		t.Fatalf("resources positional arg error = %v, stderr=%q", err, stderr.String())
	}
	if resourceRefreshInterval != 2*time.Second {
		t.Fatalf("resource refresh contract = %s, want 2s", resourceRefreshInterval)
	}
	if got := newResourceCommand().refreshInterval(); got != resourceRefreshInterval {
		t.Fatalf("default resource refresh = %s, want %s", got, resourceRefreshInterval)
	}
}

func TestResourceInspectorLocalizesEnglishAndKoreanUX(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 12, 12, 0, 2, 0, time.UTC)
	tests := []struct {
		name        string
		locale      i18n.Locale
		title       string
		prompt      string
		header      string
		footer      string
		unassigned  string
		shared      string
		other       string
		detail      string
		caveat      string
		unavailable string
		help        string
	}{
		{name: "en-US", locale: i18n.FallbackLocale, title: "Resources · Projects", prompt: "search › ", header: "Host CPU", footer: "Enter: drill down", unassigned: "Unassigned", shared: "Shared / ambiguous", other: "Other / unattributed", detail: "Processes:", caveat: "RSS sum may count shared pages", unavailable: "unavailable on darwin", help: "Usage: projmux resources\n  Open the read-only"},
		{name: "ko-KR", locale: i18n.Locale("ko-KR"), title: "리소스 · 프로젝트", prompt: "검색 › ", header: "호스트 CPU", footer: "Enter: 상세 보기", unassigned: "할당되지 않음", shared: "공유 / 모호함", other: "기타 / 귀속되지 않음", detail: "프로세스:", caveat: "RSS 합계는 공유 페이지", unavailable: "darwin에서는 리소스 귀속을 사용할 수 없습니다", help: "사용법: projmux resources\n  읽기 전용"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := newResourceViewState(func() time.Time { return now }, tt.locale)
			view.setSnapshot(resourceReadySnapshot(now.Add(-time.Second)), false)
			cmd := &resourceCommand{
				homeDir:   func() (string, error) { return "", errors.New("no config") },
				lookupEnv: func(string) string { return "" },
			}
			options := cmd.pickerOptions(view, &resourceLifecycle{trigger: make(chan struct{})})
			if options.Title != tt.title || options.Prompt != tt.prompt || options.Locale != tt.locale {
				t.Fatalf("picker chrome = title %q prompt %q locale %q", options.Title, options.Prompt, options.Locale)
			}
			var help bytes.Buffer
			printResourcesUsage(&help, view.text)
			if !strings.Contains(help.String(), tt.help) {
				t.Fatalf("localized help %q missing %q", help.String(), tt.help)
			}
			items, header, footer := renderResourceView(view)
			for _, want := range []string{tt.header, "CPU", "RSS", "1s", "ready"} {
				if tt.locale == i18n.Locale("ko-KR") && want == "ready" {
					want = "준비됨"
				}
				if !strings.Contains(header, want) {
					t.Fatalf("header %q missing %q", header, want)
				}
			}
			if !strings.Contains(footer, tt.footer) {
				t.Fatalf("footer %q missing %q", footer, tt.footer)
			}
			if !strings.Contains(header, tt.other) {
				t.Fatalf("root header %q missing non-actionable %q", header, tt.other)
			}
			for value, label := range map[string]string{
				"project:" + coreresources.ProjectUnassigned: tt.unassigned,
				"project:" + coreresources.ProjectShared:     tt.shared,
				"project:/repo/api":                          "api",
			} {
				item := pickerItemByValue(items, value)
				if item == nil || item.Label != label {
					t.Fatalf("localized roots=%#v, want stable value %q label %q", items, value, label)
				}
			}

			view.enter("project:/repo/api")
			windows, _, _ := renderResourceView(view)
			view.enter(windows[0].Value)
			panes, _, _ := renderResourceView(view)
			view.enter(panes[0].Value)
			detail, _, _ := renderResourceView(view)
			meta := strings.Join(detail[0].MetaLines, "\n")
			if !strings.Contains(meta, tt.detail) || !strings.Contains(meta, tt.caveat) || !strings.Contains(meta, "CPU") || !strings.Contains(meta, "RSS") {
				t.Fatalf("localized detail = %q", meta)
			}

			view.setSnapshot(coreresources.Snapshot{At: now, Status: coreresources.StatusUnavailable, StatusReason: "platform:darwin"}, false)
			_, unavailableHeader, _ := renderResourceView(view)
			if !strings.Contains(unavailableHeader, tt.unavailable) {
				t.Fatalf("unavailable status band %q missing %q", unavailableHeader, tt.unavailable)
			}
		})
	}
}

func TestResourceViewWarmingPartialUnavailableOverageAndOtherUnknown(t *testing.T) {
	now := time.Now()
	view := newResourceViewState(func() time.Time { return now }, i18n.FallbackLocale)
	items, header, footer := renderResourceView(view)
	if !strings.Contains(header, "warming") || !strings.Contains(header, "CPU --") || !strings.Contains(header, "Other / unattributed") || !strings.Contains(header, "Memory --") || len(items) != 2 {
		t.Fatalf("first paint header=%q items=%#v, want chrome + warming/unknown explicit Other", header, items)
	}

	snap := resourceReadySnapshot(now)
	snap.Status = coreresources.StatusPartial
	snap.StatusReason = "incomplete process or host sample"
	snap.Diagnostics.Scan = coreresources.ScanDiagnostics{SampledProcesses: 20, SkippedProcesses: 2, RaceCount: 1, PermissionCount: 1}
	snap.CPU.OtherHostSharePercent = nil
	snap.CPU.OveragePercent = 4.5
	snap.Memory.OtherBytes = nil
	snap.Memory.OverageBytes = 2048
	snap.Other.CPUHostSharePercent = nil
	snap.Other.MemoryBytes = nil
	view.setSnapshot(snap, false)
	items, header, footer = renderResourceView(view)
	if !strings.Contains(header, "Other / unattributed CPU --") || !strings.Contains(header, "Memory --") {
		t.Fatalf("Other band = %q, want overage remainder unknown rather than zero", header)
	}
	for _, want := range []string{"partial sampled=20 skipped=2 race=1 permission=1", "CPU overage 4.5%", "RSS overage 2.0 KiB"} {
		if !strings.Contains(header, want) {
			t.Fatalf("status bands = %q, missing %q", header, want)
		}
	}

	view.setSnapshot(coreresources.Snapshot{At: now, Status: coreresources.StatusUnavailable, StatusReason: "resource attribution is unavailable on darwin"}, false)
	items, header, footer = renderResourceView(view)
	if !strings.Contains(header, "unavailable") || !strings.Contains(header, "unavailable on darwin") || len(items) != 2 || !strings.Contains(footer, "Enter") {
		t.Fatalf("unavailable header=%q footer=%q items=%#v", header, footer, items)
	}

	unavailableMemory := resourceReadySnapshot(now)
	unavailableMemory.Host.MemoryAvailable = false
	view.setSnapshot(unavailableMemory, false)
	_, header, _ = renderResourceView(view)
	if !strings.Contains(header, "Memory --") || !strings.Contains(header, "RSS -- (--)") {
		t.Fatalf("memory-unavailable header = %q, want unknown rather than derived zero/value", header)
	}
}

func TestResourceLifecycleNeverOverlapsAndCloseCancelsWithoutPersistence(t *testing.T) {
	collector := &blockingResourceCollector{started: make(chan struct{}), release: make(chan struct{}), canceled: make(chan struct{})}
	view := newResourceViewState(time.Now, i18n.FallbackLocale)
	lifecycle := newResourceLifecycle(collector, time.Now, resourceRefreshInterval)
	lifecycle.start()
	done := make(chan struct{})
	go func() {
		_ = lifecycle.collect(view)
		close(done)
	}()
	<-collector.started

	secondDone := make(chan struct{})
	go func() {
		_ = lifecycle.collect(view)
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("overlapping refresh did not return immediately")
	}
	if got := collector.maxActive.Load(); got != 1 {
		t.Fatalf("max active scans = %d, want 1", got)
	}

	closed := make(chan struct{})
	go func() {
		lifecycle.close()
		close(closed)
	}()
	select {
	case <-collector.canceled:
	case <-time.After(time.Second):
		t.Fatal("close did not cancel in-flight scan")
	}
	close(collector.release)
	<-done
	<-closed
	if lifecycle.previous != nil {
		t.Fatal("close retained prior sample; inspector lifecycle must be ephemeral")
	}
}

func TestResourceCommandFirstPaintAndCustomAliasClose(t *testing.T) {
	home := t.TempDir()
	paths, err := keymapPath(func() (string, error) { return home, nil }, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepathDir(paths), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths, []byte("[bindings.\"Resources:Open\"]\nkeys = [\"M-x\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collector := &sequenceResourceCollector{snapshots: []coreresources.Snapshot{resourceReadySnapshot(time.Now())}}
	calls := 0
	cmd := &resourceCommand{
		collector: collector,
		picker: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
			calls++
			if !chromeBandsContain(options.ChromeBands, "warming") {
				t.Fatalf("first paint bands = %#v, want warming before collection", options.ChromeBands)
			}
			if !pickerActionsContain(options.Actions, "ctrl-r") || !pickerActionsContain(options.Actions, "tab") || !pickerActionsContain(options.Actions, "alt-x") {
				t.Fatalf("actions = %#v, want Ctrl-R/Tab/custom Resources alias close", options.Actions)
			}
			update, err := options.DeferredUpdate()
			if err != nil || !chromeBandsContain(update.ChromeBands, "ready") {
				t.Fatalf("deferred update = %#v err=%v, want ready", update, err)
			}
			return intpicker.Result{Key: "alt-x", Closed: true}, nil
		}),
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
		now:       time.Now,
		interval:  time.Hour,
	}
	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || collector.calls.Load() != 1 {
		t.Fatalf("picker calls=%d collector calls=%d, want one ephemeral run", calls, collector.calls.Load())
	}
}

func TestResourcesActionSettingsVisibilityAndLabsOffConfigOpen(t *testing.T) {
	home := t.TempDir()
	settings := testKeybindingSettingsCommand(t, home, func(intpickercompat.Options) (intpickercompat.Result, error) {
		return intpickercompat.Result{}, nil
	})
	entries, err := settings.keybindingEntries()
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntryValue(entries, settingsActionPrefixKeymap+"Resources:Open") || !hasEntryLabelContaining(entries, "Resources") {
		t.Fatalf("Settings keybinding entries = %#v, want Resources:Open visible", entries)
	}
	settings.lookupEnv = func(name string) string {
		if name == i18n.LocaleEnvName {
			return "ko-KR"
		}
		return ""
	}
	entries, err = settings.keybindingEntries()
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntryLabelContaining(entries, "리소스") {
		t.Fatalf("ko-KR Settings keybinding entries = %#v, want localized Resources name", entries)
	}
	detail, _, err := settings.keybindingDetailEntries("Resources:Open")
	if err != nil {
		t.Fatal(err)
	}
	if !hasEntryLabelContaining(detail, "읽기 전용 프로젝트, 창, pane 리소스 검사기 열기") {
		t.Fatalf("ko-KR Resources detail = %#v, want localized description", detail)
	}
	catalog := defaultKeyBindingCatalog()
	for i := range catalog {
		if catalog[i].ID == "Resources:Open" {
			catalog[i].PlainChords = []string{"M-u"}
		}
	}
	configText := fallbackRenderThemeSource().tmuxStandaloneConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(
		"/tmp/projmux", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, catalog, true,
	)
	if !strings.Contains(configText, "set -g "+liveResourcesTmuxOption+" off") || !strings.Contains(configText, "bind-key -n M-u run-shell \"'/tmp/projmux' tmux popup-toggle --client #{client_tty} resource-inspector\"") {
		t.Fatalf("Labs-off generated config missing independent Resources action: %s", configText)
	}
}

func resourceReadySnapshot(at time.Time) coreresources.Snapshot {
	hostCPU, attrCPU, paneCPU10, paneCPU2 := 50.0, 12.0, 10.0, 2.0
	hostPercent10, hostPercent20 := 10.0, 20.0
	otherCPU := 38.0
	otherMem := uint64(6 << 30)
	return coreresources.Snapshot{
		At: at, Status: coreresources.StatusReady, StatusReason: "ready",
		Host:     coreresources.HostSample{CPUAvailable: true, MemoryAvailable: true, LogicalCPUs: 8, MemoryTotalBytes: 8 << 30, MemoryAvailableBytes: 4 << 30},
		CPU:      coreresources.CPUReconciliation{HostBusyPercent: &hostCPU, AttributedPercent: &attrCPU, OtherHostSharePercent: &otherCPU},
		Memory:   coreresources.MemoryReconciliation{HostUsedBytes: 4 << 30, AttributedRSSBytes: 384 << 20, OtherBytes: &otherMem},
		Other:    coreresources.OtherUsage{Key: coreresources.OtherUnattributed, CPUHostSharePercent: &otherCPU, MemoryBytes: &otherMem},
		Projects: []coreresources.ProjectUsage{{Key: "/repo/api", PaneCount: 2, WindowCount: 1, ProcessCount: 5, CPU: &coreresources.CPUUsage{HostSharePercent: attrCPU, CoreEquivalent: .96}, Memory: coreresources.MemoryUsage{RSSBytes: 384 << 20, HostPercent: &hostPercent10}}},
		Windows:  []coreresources.WindowUsage{{Socket: "sock", WindowID: "@7", WindowName: "editor", Sessions: []string{"dev"}, ProjectKey: "/repo/api", PaneCount: 2, ProcessCount: 5, CPU: &coreresources.CPUUsage{HostSharePercent: attrCPU, CoreEquivalent: .96}, Memory: coreresources.MemoryUsage{RSSBytes: 384 << 20, HostPercent: &hostPercent10}}},
		Panes: []coreresources.PaneUsage{
			{Socket: "sock", WindowID: "@7", WindowName: "editor", PaneID: "%10", Sessions: []string{"dev"}, PanePID: 100, PaneTTY: "/dev/pts/1", ProjectKey: "/repo/api", ProjectAnchor: "/repo/api", PaneLabel: "user label", AIAgent: "codex", AITopic: "ignored topic", PaneCommand: "zsh", PaneTitle: "raw", ProcessCount: 3, CPU: &coreresources.CPUUsage{HostSharePercent: paneCPU10, CoreEquivalent: .8}, Memory: coreresources.MemoryUsage{RSSBytes: 128 << 20, HostPercent: &hostPercent10}},
			{Socket: "sock", WindowID: "@7", WindowName: "editor", PaneID: "%11", Sessions: []string{"dev"}, PanePID: 101, PaneTTY: "/dev/pts/2", ProjectKey: "/repo/api", ProjectAnchor: "/repo/api", AIAgent: "codex", AITopic: "agent topic", PaneCommand: "python", PaneTitle: "raw", ProcessCount: 2, CPU: &coreresources.CPUUsage{HostSharePercent: paneCPU2, CoreEquivalent: .16}, Memory: coreresources.MemoryUsage{RSSBytes: 256 << 20, HostPercent: &hostPercent20}},
		},
	}
}

type blockingResourceCollector struct {
	started   chan struct{}
	release   chan struct{}
	canceled  chan struct{}
	once      sync.Once
	active    atomic.Int32
	maxActive atomic.Int32
}

func (c *blockingResourceCollector) CollectResourceSnapshot(ctx context.Context, _ *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
	active := c.active.Add(1)
	defer c.active.Add(-1)
	for {
		old := c.maxActive.Load()
		if active <= old || c.maxActive.CompareAndSwap(old, active) {
			break
		}
	}
	c.once.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		close(c.canceled)
		<-c.release
		return coreresources.Snapshot{}, coreresources.Sample{}, ctx.Err()
	case <-c.release:
		return resourceReadySnapshot(time.Now()), coreresources.Sample{Available: true}, nil
	}
}

type sequenceResourceCollector struct {
	snapshots []coreresources.Snapshot
	calls     atomic.Int32
}

func (c *sequenceResourceCollector) CollectResourceSnapshot(context.Context, *coreresources.Sample) (coreresources.Snapshot, coreresources.Sample, error) {
	idx := int(c.calls.Add(1) - 1)
	if idx >= len(c.snapshots) {
		idx = len(c.snapshots) - 1
	}
	return c.snapshots[idx], coreresources.Sample{At: c.snapshots[idx].At, Available: true}, nil
}

func pickerItemsContain(items []intpicker.Item, text string) bool {
	return pickerItemContaining(items, text) != nil
}

func pickerItemContaining(items []intpicker.Item, text string) *intpicker.Item {
	for i := range items {
		if strings.Contains(items[i].Label, text) {
			return &items[i]
		}
	}
	return nil
}

func pickerItemByValue(items []intpicker.Item, value string) *intpicker.Item {
	for index := range items {
		if items[index].Value == value {
			return &items[index]
		}
	}
	return nil
}

func pickerActionsContain(actions []intpicker.Action, key string) bool {
	for _, action := range actions {
		if action.Key == key {
			return true
		}
	}
	return false
}

func pickerAction(actions []intpicker.Action, key string) (intpicker.Action, bool) {
	for _, action := range actions {
		if action.Key == key {
			return action, true
		}
	}
	return intpicker.Action{}, false
}

func chromeBandsContain(bands []intpicker.ChromeBand, value string) bool {
	for _, band := range bands {
		if strings.Contains(band.Label+" "+band.Value+" "+band.Secondary, value) {
			return true
		}
	}
	return false
}

func renderResourceView(view *resourceViewState) ([]intpicker.Item, string, string) {
	screen := view.screen()
	header := make([]string, 0, len(screen.bands))
	for _, band := range screen.bands {
		header = append(header, strings.TrimSpace(strings.Join([]string{band.Label, band.Value, band.Secondary}, " ")))
	}
	return screen.items, strings.Join(header, "\n"), screen.footer
}

func filepathDir(path string) string {
	idx := strings.LastIndex(path, string(os.PathSeparator))
	if idx < 0 {
		return "."
	}
	return path[:idx]
}
