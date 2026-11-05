package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const ordersCSV = bom + "Order ID,Email,Financial Status,Paid at,Amount Refunded,Created at,Lineitem name,Billing Name,Billing Phone,Cancelled at\n" +
	"5,old@example.com,PAID,3/10/2026 20:17,0,3/10/2026 20:17,Family Membership,Old Member,5551234567,\n" +
	"4,old@example.com,PAID,3/9/2025 9:00,0,3/9/2025 9:00,Individual Membership,Old Member,,\n" +
	"3,new@example.com,PAID,10/7/2026 8:03,0,10/7/2026 8:03,Individual Membership,New Payer,,\n" +
	"2,refund@example.com,PAID,4/1/2026 8:00,35,4/1/2026 8:00,Individual Membership,Refunded,,\n" +
	"1,shirt@example.com,PAID,4/1/2026 8:00,0,4/1/2026 8:00,Club Jersey,Shirt Buyer,,\n" +
	",,,,,,,,,\n"

// An orders export sets each payer's dues to end a year after their latest
// payment, keeping the year they first joined, and adds payers not on the list.
func TestImportOrders(t *testing.T) {
	a, _, _ := testApp(t)
	clubTZ = a.cfg.Location
	a.store.Import([]Member{{Name: "Old Member", Email: "old@example.com", PaidYears: []int{2024, 2025, 2027}, Joined: day("2024-06-03")}})

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "orders.csv")
	fw.Write([]byte(ordersCSV))
	mw.Close()
	req := httptest.NewRequest("POST", "/admin/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	a.importCSV(rec, req)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc, "ok=orders&n=1&added=") {
		t.Fatalf("redirect %d %q", rec.Code, loc)
	}

	old, _ := a.store.MemberByEmail("old@example.com")
	if got := old.DuesExpire().In(clubTZ).Format("2006-01-02"); got != "2027-03-10" {
		t.Errorf("old member's dues end %s, want 2027-03-10 (a year after the latest payment)", got)
	}
	if got := old.Joined.In(clubTZ).Format("2006-01-02"); got != "2024-03-10" {
		t.Errorf("old member joined %s, want 2024-03-10 (same first year, payment day)", got)
	}
	if old.Plan != "family" || len(old.PaidYears) != 3 {
		t.Errorf("old member: plan %q, years %v", old.Plan, old.PaidYears)
	}
	if !old.PaidAt(day("2027-03-10")) || old.PaidAt(day("2027-03-11")) {
		t.Error("paid through the day a year after the payment")
	}

	n, ok := a.store.MemberByEmail("new@example.com")
	if !ok || n.Name != "New Payer" || n.DuesExpire().In(clubTZ).Format("2006-01-02") != "2027-10-07" || n.Phone != "" {
		t.Errorf("new payer: %+v", n)
	}
	for _, e := range []string{"refund@example.com", "shirt@example.com"} {
		if _, ok := a.store.MemberByEmail(e); ok {
			t.Errorf("%s was added", e)
		}
	}

	req = httptest.NewRequest("GET", loc, nil)
	if f := a.ordersFlash(req); !strings.Contains(f, "1 member is") || !strings.Contains(f, "New Payer (new@example.com)") {
		t.Errorf("flash: %q", f)
	}

}
