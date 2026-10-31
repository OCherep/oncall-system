package main

import "time"

// nowFunc is the single source of "now" for schedule logic; tests replace it.
var nowFunc = time.Now

// kyivLocation returns the configured on-grid timezone (default Europe/Kyiv).
func kyivLocation() *time.Location {
	tz := "Europe/Kyiv"
	if db != nil {
		tz = getSetting("on_grid_timezone", "Europe/Kyiv")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.FixedZone("EET", 2*3600)
	}
	return loc
}

// kyivToday is today's calendar date (YYYY-MM-DD) in Kyiv, independent of container TZ.
func kyivToday() string { return kyivNow().Format("2006-01-02") }
