package postgres

import (
	"fmt"
	"time"
)

// ValidateTaskSchedule validates civil dates, with an inclusive finish. Either
// endpoint may be absent; callers validate the combined saved and patched pair.
func ValidateTaskSchedule(start, finish *string) error {
	for _, date := range []*string{start, finish} {
		if date == nil {
			continue
		}
		parsed, err := time.Parse("2006-01-02", *date)
		if err != nil || len(*date) != 10 || parsed.Year() < 1 || parsed.Year() > 9999 || parsed.Format("2006-01-02") != *date {
			return fmt.Errorf("%w: planned dates must be real YYYY-MM-DD dates in years 0001–9999", ErrTaskPlanningInput)
		}
	}
	if start != nil && finish != nil && *finish < *start {
		return fmt.Errorf("%w: planned finish must be on or after planned start", ErrTaskPlanningInput)
	}
	return nil
}

func sameOptionalDate(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
