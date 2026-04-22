package render

import "testing"

func TestBuildSessionRowsSanitizesNames(t *testing.T) {
	t.Parallel()

	rows := BuildSessionRows([]string{"repo-a", "bad\tname\nx"})
	want := []SessionRow{
		{Label: "repo-a", Value: "repo-a"},
		{Label: "bad name x", Value: "bad name x"},
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
