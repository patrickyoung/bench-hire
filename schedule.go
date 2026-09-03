package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Routine cadences are deliberately few and readable. "every" is one of the
// named cadences or a plain duration such as 45m or 2h; "at" is HH:MM in the
// controller's local time; weekday is 0 (Sunday) to 6 for weekly routines.
var namedCadences = map[string]string{
	"hourly":   "every hour, on the hour",
	"daily":    "every day",
	"weekdays": "Monday to Friday",
	"weekly":   "once a week",
}

func validateCadence(every, at string, weekday int) error {
	every = strings.TrimSpace(every)
	if every == "" {
		return fmt.Errorf("a routine needs a cadence")
	}
	if _, named := namedCadences[every]; named {
		if every != "hourly" {
			if _, _, err := parseClock(at); err != nil {
				return err
			}
		}
		if every == "weekly" && (weekday < 0 || weekday > 6) {
			return fmt.Errorf("weekday must be 0 (Sunday) to 6 (Saturday)")
		}
		return nil
	}
	d, err := time.ParseDuration(every)
	if err != nil || d < time.Minute || d > 7*24*time.Hour {
		return fmt.Errorf("cadence must be hourly, daily, weekdays, weekly, or a duration between 1m and 168h")
	}
	return nil
}

func parseClock(at string) (int, int, error) {
	parts := strings.Split(strings.TrimSpace(at), ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("time of day must be HH:MM")
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("time of day must be HH:MM")
	}
	return h, m, nil
}

// nextDue returns the first due time strictly after "after".
func nextDue(every, at string, weekday int, after time.Time, loc *time.Location) time.Time {
	after = after.In(loc)
	switch every {
	case "hourly":
		return after.Truncate(time.Hour).Add(time.Hour)
	case "daily", "weekdays", "weekly":
		h, m, err := parseClock(at)
		if err != nil {
			h, m = 9, 0
		}
		candidate := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, loc)
		for i := 0; i < 15; i++ {
			if candidate.After(after) {
				switch every {
				case "daily":
					return candidate
				case "weekdays":
					if wd := candidate.Weekday(); wd != time.Saturday && wd != time.Sunday {
						return candidate
					}
				case "weekly":
					if int(candidate.Weekday()) == weekday {
						return candidate
					}
				}
			}
			candidate = candidate.AddDate(0, 0, 1)
		}
		return candidate
	}
	d, err := time.ParseDuration(every)
	if err != nil || d < time.Minute {
		d = time.Hour
	}
	// Align to multiples of the interval in UTC so the same wall clock always
	// produces the same occurrence id.
	utc := after.UTC()
	return utc.Truncate(d).Add(d).In(loc)
}

func describeCadence(every, at string, weekday int) string {
	switch every {
	case "hourly":
		return "every hour"
	case "daily":
		return "daily at " + at
	case "weekdays":
		return "weekdays at " + at
	case "weekly":
		return time.Weekday(weekday).String() + "s at " + at
	}
	return "every " + every
}

// occurrenceID makes the same due time map to the same request id, so a
// scheduler that runs twice cannot queue one occurrence twice.
func occurrenceID(routineID string, due time.Time) string {
	return routineID + "-" + due.UTC().Format("20060102t1504")
}
