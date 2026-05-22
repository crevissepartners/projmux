package aibadge

import "testing"

func TestAggregatePriorityContract(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		current string
		next    string
		want    string
	}{
		{name: "prompt beats complete", current: ResponseComplete, next: ApprovalRequired, want: ApprovalRequired},
		{name: "input beats progress", current: InProgress, next: InputRequired, want: InputRequired},
		{name: "complete beats progress", current: InProgress, next: ResponseComplete, want: ResponseComplete},
		{name: "progress does not beat complete", current: ResponseComplete, next: InProgress, want: ResponseComplete},
		{name: "none yields progress", current: "", next: InProgress, want: InProgress},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Aggregate(tt.current, tt.next); got != tt.want {
				t.Fatalf("Aggregate(%q, %q) = %q, want %q", tt.current, tt.next, got, tt.want)
			}
		})
	}
}

func TestStyleAndThemeRoleContracts(t *testing.T) {
	t.Parallel()

	if got := NormalizeStyle("minimal"); got != StyleOff {
		t.Fatalf("NormalizeStyle(minimal) = %q, want %q", got, StyleOff)
	}
	if got := NormalizeStyle("unknown"); got != StyleDot {
		t.Fatalf("NormalizeStyle(unknown) = %q, want %q", got, StyleDot)
	}

	cases := []struct {
		kind string
		role string
	}{
		{ApprovalRequired, RoleActionRequired},
		{InputRequired, RoleActionRequired},
		{ResponseComplete, RoleSuccess},
		{InProgress, RoleProgress},
		{"", ""},
		{"future", ""},
	}
	for _, tt := range cases {
		if got := ThemeRole(tt.kind); got != tt.role {
			t.Fatalf("ThemeRole(%q) = %q, want %q", tt.kind, got, tt.role)
		}
	}
}
