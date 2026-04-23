package render

import "testing"

func TestBuildSessionRowsSanitizesNames(t *testing.T) {
	t.Parallel()

	rows := BuildSessionRows([]SessionSummary{
		{Name: "repo-a", Attached: true, WindowCount: 2, PaneCount: 3, StoredTarget: "w3.p1", Path: "/tmp/repo-a"},
		{Name: "bad\tname\nx", Attached: false, WindowCount: 1, PaneCount: 1, StoredTarget: "w1", Path: "/tmp/bad\tpath\nx"},
	})
	want := []SessionRow{
		{Label: "repo-a  [attached]  2w  3p  w3.p1  /tmp/repo-a", Value: "repo-a"},
		{Label: "bad name x  [detached]  1w  1p  w1  /tmp/bad path x", Value: "bad name x"},
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
