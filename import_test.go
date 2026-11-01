package main

import (
	"reflect"
	"strings"
	"testing"
)

// A Squarespace profiles export gives the first and last order dates and how
// many orders there were; the import works out every year dues were paid.
func TestImportSquarespacePaidYears(t *testing.T) {
	csv := `"Email","First Name","Last Name","Created On","Order Count","Last Order Date","Member Since","Member Areas","Customer Since","Accepts Email Marketing"
"three@example.com","Three","Years","February 25, 2024 at 2:25:28 PM EST","3","March 1, 2026 at 9:00:00 AM EST","March 2, 2024 at 9:00:00 AM EST","Individual Membership","March 2, 2024 at 9:00:00 AM EST","true"
"gap@example.com","Gap","Year","February 25, 2024 at 2:25:28 PM EST","2","March 1, 2026 at 9:00:00 AM EST","","Family Membership","March 2, 2024 at 9:00:00 AM EST","true"
"lapsed@example.com","Lapsed","Rider","February 25, 2024 at 2:25:28 PM EST","2","March 1, 2025 at 9:00:00 AM EST","","","March 2, 2024 at 9:00:00 AM EST","true"
"once@example.com","Once","Only","February 25, 2024 at 2:25:28 PM EST","1","April 16, 2026 at 8:26:56 AM EDT","","Individual Membership","April 16, 2026 at 8:26:56 AM EDT","true"
"none@example.com","No","Orders","February 25, 2024 at 2:25:28 PM EST","0","","","","","false"
`
	rows, bad, err := parseImport(strings.NewReader(csv))
	if err != nil || bad != 0 {
		t.Fatalf("parseImport: bad %d, err %v", bad, err)
	}
	want := map[string][]int{
		"three@example.com":  {2024, 2025, 2026},
		"gap@example.com":    {2024, 2026},
		"lapsed@example.com": {2024, 2025},
		"once@example.com":   {2026},
		"none@example.com":   nil,
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for _, r := range rows {
		if !reflect.DeepEqual(r.PaidYears, want[r.Email]) {
			t.Errorf("%s: paid years %v, want %v", r.Email, r.PaidYears, want[r.Email])
		}
	}
	if !rows[2].paidBefore || rows[2].membership {
		t.Errorf("lapsed member: paidBefore %v, membership %v", rows[2].paidBefore, rows[2].membership)
	}
}
