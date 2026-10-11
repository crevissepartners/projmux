package profiles

import (
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/profile"
)

func TestProfileTableFieldCoverage(t *testing.T) {
	// Store-only Path and Detail do not have CLI table columns. Every other
	// stored field participates in the projection; additions require review.
	typ := reflect.TypeFor[profile.Entry]()
	fields := []string{"Project", "Name", "Source", "Path", "Provider", "Instructions", "Model", "Effort", "Roles", "Digest", "Valid", "Reason", "Detail"}
	got := make([]string, typ.NumField())
	for i := range got {
		got[i] = typ.Field(i).Name
	}
	if !reflect.DeepEqual(got, fields) {
		t.Fatalf("profile.Entry fields changed: %v", got)
	}
	entry := profile.Entry{Project: "proj-example", Name: "reader", Source: "user", Path: "store-only", Provider: "claude", Instructions: "guide", Model: "opus", Effort: "high", Roles: []string{"reader", "writer"}, Digest: "sha256:example", Valid: false, Reason: "profile-role-claimed", Detail: "store-only"}
	want := Info{"reader", "user", "claude", "guide", "opus", "high", "reader,writer", "sha256:example", "no (profile-role-claimed)", "proj-example"}
	if got := Project(entry); got != want {
		t.Fatalf("projection %+v vs %+v", got, want)
	}
	blank := Project(profile.Entry{Name: "empty", Source: "user", Valid: true})
	if blank != (Info{"empty", "user", "-", "-", "-", "-", "-", "-", "yes", "global"}) {
		t.Fatalf("blank cells %+v", blank)
	}
}
