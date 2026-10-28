// Package visits records learner visits and computes the two attendance
// figures (AC-11). Rules: docs/implementation-decisions.md, «Посещаемость».
package visits

import (
	"context"
	"time"
	_ "time/tzdata" // the distroless image has no zoneinfo

	"github.com/jackc/pgx/v5/pgxpool"
)

// Zone defines calendar days for attendance.
const Zone = "Europe/Moscow"

var zone = mustZone()

func mustZone() *time.Location {
	loc, err := time.LoadLocation(Zone)
	if err != nil {
		panic(err)
	}
	return loc
}

type Attendance struct {
	TotalLearners   int       `json:"totalLearners"`
	RegularlyActive int       `json:"regularlyActive"`
	Timezone        string    `json:"timezone"`
	WindowStart     time.Time `json:"windowStart"`
	WindowEnd       time.Time `json:"windowEnd"`
}

// Record stores one authenticated learner request.
func Record(ctx context.Context, db *pgxpool.Pool, accountID string, at time.Time) error {
	_, err := db.Exec(ctx, `insert into visits (account_id, at) values ($1, $2)`, accountID, at)
	return err
}

// Count returns figures for the window D-6…D, where D is the Moscow day of now:
// [00:00 D-6, 00:00 D+1). Teachers are excluded by their current flag.
func Count(ctx context.Context, db *pgxpool.Pool, now time.Time) (Attendance, error) {
	d := now.In(zone)
	a := Attendance{Timezone: Zone,
		WindowStart: time.Date(d.Year(), d.Month(), d.Day()-6, 0, 0, 0, 0, zone),
		WindowEnd:   time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, zone)}
	err := db.QueryRow(ctx, `
		select
			(select count(distinct v.account_id) from visits v
				join accounts acc on acc.id = v.account_id where not acc.is_teacher),
			(select count(*) from (
				select v.account_id from visits v
				join accounts acc on acc.id = v.account_id
				where not acc.is_teacher and v.at >= $1 and v.at < $2
				group by v.account_id
				having count(distinct (v.at at time zone $3)::date) >= 2) r)`,
		a.WindowStart, a.WindowEnd, Zone).Scan(&a.TotalLearners, &a.RegularlyActive)
	return a, err
}
