package app

import (
	"reflect"
	"testing"
)

func TestInstallReplacementTargetsOnlyNameRemainingDrainableImages(t *testing.T) {
	t.Parallel()
	const self = "/test/bin/projmux"
	broker := []string{self, "internal", "codex-broker", "serve"}
	images := []codexProcessImage{
		{PID: 8, Exe: self + procDeletedSuffix, Cmdline: broker},
		{PID: 7, Exe: self + procDeletedSuffix, Cmdline: broker},
		{PID: 6, Exe: self, Cmdline: broker}, // already on the installed image
		{PID: 5, Exe: "/other/projmux" + procDeletedSuffix, Cmdline: broker},
		{PID: 4, Exe: self + procDeletedSuffix, Cmdline: []string{self, "internal", "agent-hook", "ingest", "codex-broker-watch"}},
		{PID: 3, Exe: self + procDeletedSuffix, Cmdline: []string{self, "shell"}},
		{PID: 2, Exe: self + procDeletedSuffix, Cmdline: broker}, // the reader
		{PID: 0, Exe: self + procDeletedSuffix, Cmdline: broker},
	}
	var revisionReads []int
	got := projectInstallReplacementTargets(self, 2, images, func(image codexProcessImage) string {
		revisionReads = append(revisionReads, image.PID)
		if image.PID == 7 {
			return "8ae6e563"
		}
		return ""
	})
	want := []installReplacementTarget{
		{role: codexControlPlaneRoleBroker, pid: 7, revision: "8ae6e563"},
		{role: codexControlPlaneRoleBroker, pid: 8, revision: "unknown"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v, want %+v", got, want)
	}
	if !reflect.DeepEqual(revisionReads, []int{8, 7}) {
		t.Fatalf("read revisions for non-targets: %v", revisionReads)
	}
	if got := projectInstallReplacementTargets("", 2, images, nil); len(got) != 0 {
		t.Fatalf("unknown executable named targets: %+v", got)
	}
	if got := installReplacementProcessRevision(codexProcessImage{PID: -1}); got != "" {
		t.Fatalf("unreadable process revision = %q, want unknown", got)
	}
}
