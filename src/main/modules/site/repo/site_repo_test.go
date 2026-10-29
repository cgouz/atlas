package repo

import "testing"

type fakeRow struct{ n int }

func (r *fakeRow) Scan(dest ...any) error {
	r.n = len(dest)
	return nil
}

func TestScanReadsEveryColumn(t *testing.T) {
	var row fakeRow
	if _, err := scan(&row); err != nil {
		t.Fatal(err)
	}
	// Must match the column list selected in every site query.
	if row.n != 16 {
		t.Fatalf("scan reads %d columns, queries select 16", row.n)
	}
}
