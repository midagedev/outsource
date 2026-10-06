package human

import (
	"testing"
	"time"
)

// The same instant reads "17:23" on the viewer's day and carries its date on
// any other. FAIL-first: always formatting "15:04" renders the next-day case
// as a bare "17:23", which reads as today.
func TestClockSameDayAndOtherDay(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("KST", 9*60*60)
	t.Cleanup(func() { time.Local = old })
	reset := time.Date(2026, 10, 6, 8, 23, 45, 0, time.UTC) // 17:23:45 KST
	for _, c := range []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 10, 6, 6, 30, 41, 0, time.UTC), "17:23"},       // 15:30 KST, same day
		{time.Date(2026, 10, 5, 16, 0, 0, 0, time.UTC), "17:23"},        // 01:00 KST on the 6th — UTC says the 5th
		{time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC), "Oct 6 17:23"},  // 21:00 KST the day before
		{time.Date(2027, 10, 6, 6, 30, 41, 0, time.UTC), "Oct 6 17:23"}, // a year later, same day number
	} {
		if got := Clock(reset, c.now); got != c.want {
			t.Errorf("Clock(%s, now=%s) = %q, want %q", reset.Format(time.RFC3339), c.now.Format(time.RFC3339), got, c.want)
		}
	}
}
