// Package darwin consumes National Rail's Darwin Push Port: live forecasts,
// platforms, cancellations and delay reasons, plus its reference data.
package darwin

import (
	"encoding/xml"
	"strconv"
	"strings"
)

// Pport is a Push Port message. Elements are matched by local name, so the
// parser works across schema versions whose namespaces differ.
type Pport struct {
	TS      string  `xml:"ts,attr"`
	Version string  `xml:"version,attr"`
	UR      *Update `xml:"uR"`
	SR      *Update `xml:"sR"`
}

// Update is the uR (update) or sR (snapshot) payload.
type Update struct {
	UpdateOrigin string        `xml:"updateOrigin,attr"`
	TrainStatus  []TrainStatus `xml:"TS"`
	Schedules    []Schedule    `xml:"schedule"`
	Deactivated  []struct {
		RID string `xml:"rid,attr"`
	} `xml:"deactivated"`
	Associations []Association    `xml:"association"`
	Messages     []StationMessage `xml:"OW"`
}

// Association is Darwin's live view of a join (JJ), divide (VV), next
// working (NP) or link (LK) between two trains.
type Association struct {
	TIPLOC    string `xml:"tiploc,attr"`
	Category  string `xml:"category,attr"`
	Cancelled bool   `xml:"isCancelled,attr"`
	Deleted   bool   `xml:"isDeleted,attr"`
	Main      struct {
		RID string `xml:"rid,attr"`
	} `xml:"main"`
	Assoc struct {
		RID string `xml:"rid,attr"`
	} `xml:"assoc"`
}

// TrainStatus (TS) carries forecasts and actuals for some of a train's
// locations.
type TrainStatus struct {
	RID        string       `xml:"rid,attr"`
	UID        string       `xml:"uid,attr"`
	SSD        string       `xml:"ssd,attr"`
	LateReason *Reason      `xml:"LateReason"`
	Locations  []TSLocation `xml:"Location"`
}

// TSLocation identifies a schedule location by TIPLOC and its working times.
type TSLocation struct {
	TPL  string    `xml:"tpl,attr"`
	WTA  string    `xml:"wta,attr"`
	WTD  string    `xml:"wtd,attr"`
	WTP  string    `xml:"wtp,attr"`
	PTA  string    `xml:"pta,attr"`
	PTD  string    `xml:"ptd,attr"`
	Arr  *Forecast `xml:"arr"`
	Dep  *Forecast `xml:"dep"`
	Pass *Forecast `xml:"pass"`
	Plat *Plat     `xml:"plat"`
}

// Forecast is an arrival, departure or pass forecast.
type Forecast struct {
	ET        string `xml:"et,attr"`
	WET       string `xml:"wet,attr"`
	AT        string `xml:"at,attr"`
	ATRemoved bool   `xml:"atRemoved,attr"`
	ETUnknown bool   `xml:"etUnknown,attr"`
	Delayed   bool   `xml:"delayed,attr"`
	Src       string `xml:"src,attr"`
}

// Plat is a platform forecast. Suppressed platforms must not be shown to the
// public.
type Plat struct {
	Number     string `xml:",chardata"`
	PlatSup    bool   `xml:"platsup,attr"`
	CISPlatSup bool   `xml:"cisPlatsup,attr"`
	Conf       bool   `xml:"conf,attr"`
}

// Suppressed reports whether the platform must be hidden.
func (p *Plat) Suppressed() bool { return p.PlatSup || p.CISPlatSup }

// Reason is a late-running or cancellation reason code.
type Reason struct {
	Code   string `xml:",chardata"`
	TIPLOC string `xml:"tiploc,attr"`
	Near   bool   `xml:"near,attr"`
}

// Int returns the reason code, or 0 if it is missing or malformed.
func (r *Reason) Int() int {
	if r == nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(r.Code))
	return n
}

// Schedule is Darwin's view of a train's schedule. We take our timetable from
// Network Rail, so only the cancellation information is used.
type Schedule struct {
	RID          string        `xml:"rid,attr"`
	UID          string        `xml:"uid,attr"`
	SSD          string        `xml:"ssd,attr"`
	TOC          string        `xml:"toc,attr"`
	Deleted      bool          `xml:"deleted,attr"`
	IsPassenger  *bool         `xml:"isPassengerSvc,attr"`
	CancelReason *Reason       `xml:"cancelReason"`
	Locations    []ScheduleLoc `xml:",any"`
}

// ScheduleLoc is an OR, OPOR, IP, OPIP, PP, DT or OPDT element.
type ScheduleLoc struct {
	XMLName xml.Name
	TPL     string `xml:"tpl,attr"`
	WTA     string `xml:"wta,attr"`
	WTD     string `xml:"wtd,attr"`
	WTP     string `xml:"wtp,attr"`
	Can     bool   `xml:"can,attr"`
}

// Parse decodes one Push Port message.
func Parse(b []byte) (*Pport, error) {
	var p Pport
	if err := xml.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// parseTime parses Darwin's "HH:MM" or "HH:MM:SS" into seconds after
// midnight.
func parseTime(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || len(p) != 2 {
			return 0, false
		}
		v[i] = n
	}
	if v[0] > 23 || v[1] > 59 || v[2] > 59 {
		return 0, false
	}
	return v[0]*3600 + v[1]*60 + v[2], true
}

// resolve places a Darwin time-of-day nearest to a reference time measured in
// seconds after midnight of the run date (which may exceed 86400).
func resolve(tod, ref int) int {
	v := ref - ref%86400 + tod
	switch {
	case v-ref > 12*3600:
		v -= 86400
	case ref-v > 12*3600:
		v += 86400
	}
	return v
}
