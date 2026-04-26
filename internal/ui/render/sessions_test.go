package render

import "testing"

func TestBuildSessionRowsSanitizesNames(t *testing.T) {
	t.Parallel()

	rows := BuildSessionRows([]SessionSummary{
		{Name: "repo-a", Attached: true, WindowCount: 2, PaneCount: 3, StoredTarget: "w3.p1", Path: "/tmp/repo-a"},
		{Name: "bad\tname\nx", Attached: false, WindowCount: 1, PaneCount: 1, StoredTarget: "w1", Path: "/tmp/bad\tpath\nx"},
	})
	want := []SessionRow{
		{Label: "[ ]  \x1b[32m[Attached]\x1b[0m  \x1b[34m2 Windows\x1b[0m  repo-a", Value: "repo-a"},
		{Label: "[ ]  \x1b[33m[Detached]\x1b[0m  bad name x", Value: "bad name x"},
	}

	if len(rows) != len(want) {
		t.Fatalf("rows len = %d, want %d", len(rows), len(want))
	}
	for i := range rows {
		if rows[i] != want[i] {
			t.Fatalf("row[%d] = %#v, want %#v", i, rows[i], want[i])
		}
	}
}
