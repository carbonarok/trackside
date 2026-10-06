package history

import (
	"context"
	"time"
)

// Claim is the outcome of checking a journey for Delay Repay.
type Claim struct {
	// Booked is the train the passenger planned to catch.
	Booked Journey
	// Used is the train that actually got them there: the booked one, or
	// the next one that ran if the booked train was cancelled.
	Used *Journey
	// Cancelled is set when the booked train did not run between the two
	// stations.
	Cancelled bool
	// ArrivedAt is when the passenger reached their destination, nil if no
	// arrival has been reported (yet).
	ArrivedAt *time.Time
	// DelayMinutes is the arrival delay at the destination against the
	// booked arrival, nil when it can't be worked out.
	DelayMinutes *int
	// Band is the Delay Repay 15 band: "", "15-29", "30-59", "60-119" or
	// "120+".
	Band string
}

// alternativeWindow is how long after a cancelled train to look for the
// next one.
const alternativeWindow = 6 * time.Hour

// Repay checks a journey for Delay Repay. Under the national Delay Repay
// rules the delay is measured at the destination, against the booked
// arrival; when a train is cancelled, it is how late the next train that
// ran got the passenger there.
func (q *Querier) Repay(ctx context.Context, from, to []string, bookedDep time.Time) (*Claim, error) {
	// Booked times are whole minutes; allow the request to be up to a
	// minute off.
	journeys, err := q.journeys(ctx, from, to, bookedDep.Add(-time.Minute), bookedDep.Add(alternativeWindow))
	if err != nil {
		return nil, err
	}
	var booked *Journey
	for i := range journeys {
		d := journeys[i].From.BookedDep()
		if d.Sub(bookedDep).Abs() <= time.Minute {
			booked = &journeys[i]
			break
		}
	}
	if booked == nil {
		return nil, ErrNoTrain
	}
	c := &Claim{Booked: *booked}
	used := booked
	if booked.Cancelled() {
		c.Cancelled = true
		used = nil
		for i := range journeys {
			j := &journeys[i]
			if j.From.BookedDep().After(*booked.From.BookedDep()) && !j.Cancelled() && j.To.ActualArr != nil {
				used = j
				break
			}
		}
	}
	if used == nil {
		return c, nil
	}
	c.Used = used
	if used.To.ActualArr == nil {
		return c, nil
	}
	c.ArrivedAt = used.To.ActualArr
	delay := int(used.To.ActualArr.Sub(*booked.To.BookedArr()).Round(time.Minute).Minutes())
	c.DelayMinutes = &delay
	c.Band = Band(delay)
	return c, nil
}

// Band returns the Delay Repay 15 band for a delay in minutes.
func Band(minutes int) string {
	switch {
	case minutes >= 120:
		return "120+"
	case minutes >= 60:
		return "60-119"
	case minutes >= 30:
		return "30-59"
	case minutes >= 15:
		return "15-29"
	}
	return ""
}

// Compensation is the standard Delay Repay 15 refund for a band, as a
// percentage of the single and return fares. Operators set their own
// schemes, so this is a guide, not a promise.
func Compensation(band string) (single, ret int) {
	switch band {
	case "15-29":
		return 25, 12
	case "30-59":
		return 50, 25
	case "60-119":
		return 100, 50
	case "120+":
		return 100, 100
	}
	return 0, 0
}
