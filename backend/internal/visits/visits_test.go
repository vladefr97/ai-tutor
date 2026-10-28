package visits_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vladefr97/tutor-app/backend/internal/testdb"
	"github.com/vladefr97/tutor-app/backend/internal/visits"
)

var msk = time.FixedZone("MSK", 3*3600)

// now is D = 2026-10-28 at 15:00 Moscow time.
var now = time.Date(2026, 10, 28, 15, 0, 0, 0, msk)

func day(offset, hour, min int) time.Time {
	return time.Date(2026, 10, 28+offset, hour, min, 0, 0, msk)
}

func account(t *testing.T, db *pgxpool.Pool, email string, teacher bool) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `insert into accounts (email, is_teacher) values ($1, $2) returning id`,
		email, teacher).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func visit(t *testing.T, db *pgxpool.Pool, id string, at ...time.Time) {
	t.Helper()
	for _, a := range at {
		if err := visits.Record(context.Background(), db, id, a); err != nil {
			t.Fatal(err)
		}
	}
}

func attendance(t *testing.T, db *pgxpool.Pool, at time.Time) visits.Attendance {
	t.Helper()
	got, err := visits.Count(context.Background(), db, at)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestAttendanceExampleFromAC11(t *testing.T) {
	db := testdb.New(t)
	visit(t, db, account(t, db, "a@x.ru", false), day(-6, 10, 0), day(-1, 10, 0))
	visit(t, db, account(t, db, "b@x.ru", false), day(-2, 9, 0), day(-2, 18, 0))
	visit(t, db, account(t, db, "c@x.ru", false), day(-7, 12, 0))
	visit(t, db, account(t, db, "e@x.ru", false), day(-6, 12, 0), day(0, 12, 0))

	got := attendance(t, db, now)
	if got.TotalLearners != 4 || got.RegularlyActive != 2 {
		t.Fatalf("want total 4, regular 2; got %+v", got)
	}
	if !got.WindowStart.Equal(day(-6, 0, 0)) || !got.WindowEnd.Equal(day(1, 0, 0)) {
		t.Fatalf("window [%s, %s)", got.WindowStart, got.WindowEnd)
	}
}

func TestWindowBoundaryUsesMoscowDays(t *testing.T) {
	db := testdb.New(t)
	// 23:59 on D-7 is outside the window, 00:00 on D-6 is inside: one day only.
	visit(t, db, account(t, db, "edge@x.ru", false), day(-7, 23, 59), day(-6, 0, 0))
	// 22:30 UTC on Oct 25 (D-3) is already 01:30 on D-2 in Moscow: with D-3 noon that is two days.
	visit(t, db, account(t, db, "late@x.ru", false), day(-3, 12, 0), time.Date(2026, 10, 25, 22, 30, 0, 0, time.UTC))
	// Same Moscow day, different UTC dates (01:00 MSK is 22:00 UTC the day before): one day.
	visit(t, db, account(t, db, "same@x.ru", false), day(-1, 1, 0), day(-1, 23, 0))
	// A visit after now on D still counts as day D; tomorrow does not.
	visit(t, db, account(t, db, "edge2@x.ru", false), day(0, 23, 59), day(1, 0, 0))

	got := attendance(t, db, now)
	if got.TotalLearners != 4 || got.RegularlyActive != 1 {
		t.Fatalf("want total 4, regular 1 (only late); got %+v", got)
	}
}

func TestTeachersAreNotCounted(t *testing.T) {
	db := testdb.New(t)
	visit(t, db, account(t, db, "t@x.ru", true), day(-1, 10, 0), day(0, 10, 0))
	// A learner promoted later: earlier visits stop counting too.
	promoted := account(t, db, "p@x.ru", false)
	visit(t, db, promoted, day(-1, 10, 0), day(0, 10, 0))
	if _, err := db.Exec(context.Background(), `update accounts set is_teacher = true where id = $1`, promoted); err != nil {
		t.Fatal(err)
	}
	if got := attendance(t, db, now); got.TotalLearners != 0 || got.RegularlyActive != 0 {
		t.Fatalf("teachers counted: %+v", got)
	}
}
