package timetable

import (
	"time"

	"github.com/carbonarok/trackside/internal/ukrail"
)

// Status summarises where a service is.
type Status string

const (
	StatusScheduled          Status = "scheduled"
	StatusActivated          Status = "activated"
	StatusRunning            Status = "running"
	StatusTerminated         Status = "terminated"
	StatusCancelled          Status = "cancelled"
	StatusPartiallyCancelled Status = "partially_cancelled"
)

// annotate derives cancellations and estimated times from schedule and live
// data. Darwin, where present, takes precedence for estimates and platforms:
// its licence requires forecasts to be consistent with Darwin's.
func (s *Service) annotate() {
	s.applyDarwinActuals()
	s.applyCancellations()
	s.applyEstimates()
	s.applyDarwinForecasts()
	s.applyPosition()
}

// applyPosition marks the stop the train is standing at: the latest stop
// with an arrival but no departure, before its destination.
func (s *Service) applyPosition() {
	for i := len(s.Stops) - 2; i >= 0; i-- {
		p := &s.Stops[i]
		if p.ActualDep != nil || p.ActualPass != nil {
			return
		}
		if p.ActualArr != nil {
			p.AtPlatform = !p.Cancelled()
			return
		}
	}
}

// applyDarwinActuals fills gaps in TRUST actuals with Darwin's, and takes
// Darwin's cancellations and platforms.
func (s *Service) applyDarwinActuals() {
	for i := range s.Stops {
		p := &s.Stops[i]
		if p.ActualPlatform != "" {
			p.PlatformConfirmed = true
		}
		d := p.Darwin
		if d == nil {
			continue
		}
		if p.ActualArr == nil {
			p.ActualArr = d.ArrAT
		}
		if p.ActualDep == nil {
			p.ActualDep = d.DepAT
		}
		if p.ActualPass == nil {
			p.ActualPass = d.PassAT
		}
		p.ArrCancelled = p.ArrCancelled || d.ArrCancelled
		p.DepCancelled = p.DepCancelled || d.DepCancelled
		if d.Platform != "" {
			p.ActualPlatform = d.Platform
			p.PlatformConfirmed = d.PlatformConfirmed
		}
		p.PlatformSuppressed = d.PlatformSuppressed
	}
}

// applyDarwinForecasts replaces projected estimates with Darwin's.
func (s *Service) applyDarwinForecasts() {
	for i := range s.Stops {
		p := &s.Stops[i]
		d := p.Darwin
		if d == nil {
			continue
		}
		if p.ActualArr == nil && !p.ArrCancelled {
			if d.ArrET != nil {
				p.EstArr = d.ArrET
			}
			p.ArrDelayed = d.ArrDelayed
		}
		if p.ActualDep == nil && !p.DepCancelled {
			if d.DepET != nil {
				p.EstDep = d.DepET
			}
			p.DepDelayed = d.DepDelayed
		}
		if p.ActualPass == nil && d.PassET != nil && !p.Cancelled() {
			p.EstPass = d.PassET
		}
	}
}

func (s *Service) applyCancellations() {
	if s.PlannedCancel {
		s.cancelAll()
		return
	}
	if s.OriginSTANOX != "" {
		if i := s.firstAt(s.OriginSTANOX, 0); i > 0 {
			for j := 0; j < i; j++ {
				s.Stops[j].ArrCancelled, s.Stops[j].DepCancelled = true, true
			}
			s.Stops[i].ArrCancelled = true
			s.Stops[i].StartsHere = true
		}
	}
	if s.CancelSTANOX == "" {
		return
	}
	switch s.CancelType {
	case "AT ORIGIN", "OUT OF PLAN":
		s.cancelAll()
		return
	}
	// An en-route cancellation takes effect from the reported location: the
	// train is cut short there and the remaining stops are not served.
	i := s.firstAt(s.CancelSTANOX, 0)
	if i < 0 || i == 0 {
		s.cancelAll()
		return
	}
	s.Stops[i].DepCancelled = true
	s.Stops[i].TerminatesHere = true
	for j := i + 1; j < len(s.Stops); j++ {
		s.Stops[j].ArrCancelled, s.Stops[j].DepCancelled = true, true
	}
}

func (s *Service) cancelAll() {
	for i := range s.Stops {
		s.Stops[i].ArrCancelled, s.Stops[i].DepCancelled = true, true
	}
}

func (s *Service) firstAt(stanox string, from int) int {
	for i := from; i < len(s.Stops); i++ {
		if s.Stops[i].Location.STANOX == stanox {
			return i
		}
	}
	return -1
}

// applyEstimates projects the most recent reported delay onto the stops the
// train has not reached yet. Trains are assumed not to depart early, so an
// estimated departure is never before the booked one.
//
// This is a simple model; Darwin forecasts will replace it where available.
func (s *Service) applyEstimates() {
	last, delay := -1, time.Duration(0)
	for i := range s.Stops {
		p := &s.Stops[i]
		for _, ev := range []struct {
			actual *time.Time
			wtt    *int
		}{{p.ActualArr, p.WTTArr}, {p.ActualPass, p.WTTPass}, {p.ActualDep, p.WTTDep}} {
			if ev.actual != nil && ev.wtt != nil {
				last, delay = i, ev.actual.Sub(s.at(*ev.wtt))
			}
		}
	}
	if last < 0 || s.Status() == StatusCancelled {
		return
	}
	if delay < 0 {
		delay = 0
	}
	for i := last; i < len(s.Stops); i++ {
		p := &s.Stops[i]
		if p.ActualArr == nil && !p.ArrCancelled {
			p.EstArr = s.estimate(p.WTTArr, p.GBTTArr, delay, false)
		}
		if p.ActualDep == nil && !p.DepCancelled {
			p.EstDep = s.estimate(p.WTTDep, p.GBTTDep, delay, true)
		}
		if p.ActualPass == nil && !p.Cancelled() {
			p.EstPass = s.estimate(p.WTTPass, nil, delay, false)
		}
	}
}

func (s *Service) estimate(wtt, gbtt *int, delay time.Duration, notEarlierThanPublic bool) *time.Time {
	if wtt == nil {
		return nil
	}
	t := s.at(*wtt).Add(delay).Truncate(time.Minute)
	if notEarlierThanPublic && gbtt != nil {
		if pub := s.at(*gbtt); t.Before(pub) {
			t = pub
		}
	}
	return &t
}

func (s *Service) at(secs int) time.Time { return ukrail.AtRunDate(s.RunDate, secs) }

// Status reports the overall state of the service.
func (s *Service) Status() Status {
	cancelled, served := 0, 0
	for i := range s.Stops {
		if s.Stops[i].Cancelled() {
			cancelled++
		} else {
			served++
		}
	}
	switch {
	case served == 0 && cancelled > 0:
		return StatusCancelled
	case cancelled > 0:
		return StatusPartiallyCancelled
	}
	if n := len(s.Stops); n > 0 && s.Stops[n-1].ActualArr != nil {
		return StatusTerminated
	}
	for i := range s.Stops {
		p := &s.Stops[i]
		if p.ActualArr != nil || p.ActualDep != nil || p.ActualPass != nil {
			return StatusRunning
		}
	}
	if s.TrustID != "" {
		return StatusActivated
	}
	return StatusScheduled
}

// At returns the run-date-relative time as an absolute UK local time.
func (s *Service) At(secs *int) *time.Time {
	if secs == nil {
		return nil
	}
	t := s.at(*secs)
	return &t
}
