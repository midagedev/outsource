package human

import "time"

// Clock renders an instant as the viewer's local wall-clock time: "17:23" on
// the viewer's own day, "Oct 7 17:23" on any other. A bare hh:mm for a plan
// reset a day (or a week) away reads as "later today", which is the wrong
// answer to "when can this round go on".
//
// now is passed in rather than read, so a test pins the day; the zone is
// time.Local, which a test pins by setting it.
func Clock(t, now time.Time) string {
	lt, ln := t.In(time.Local), now.In(time.Local)
	if lt.Year() == ln.Year() && lt.YearDay() == ln.YearDay() {
		return lt.Format("15:04")
	}
	return lt.Format("Jan 2 15:04")
}
