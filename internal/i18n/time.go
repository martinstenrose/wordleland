package i18n

import "time"

// Timestamp spells an instant out on the server's clock, with the offset:
// 2026-09-23 05:29:14 +0200. It is the form the CLI and the admin pages use
// for a time of day, and the password reset email, whose reader may need to
// know exactly when a request they did not make arrived.
//
// Converted here, not before: the database hands every timestamp back in
// UTC, and the server's zone (the container's TZ) is applied only when a
// person reads it. The offset rather than a zone name, because it is the
// part that can be compared with another clock without looking anything up.
func Timestamp(t time.Time) string {
	return timestampIn(t, time.Local)
}

func timestampIn(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02 15:04:05 -0700")
}

// ClockTime is Timestamp without the date, for a list that already groups
// by day.
func ClockTime(t time.Time) string {
	return clockIn(t, time.Local)
}

func clockIn(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("15:04:05 -0700")
}
