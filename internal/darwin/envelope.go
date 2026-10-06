package darwin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Darwin reaches the Rail Data Marketplace through a JMS bridge, so each
// Kafka record is a JSON envelope. Its "bytes" field carries the Push Port
// message: a JSON rendering on the -JSON topic, XML on the -XML topic, or
// base64-encoded XML on the base topic.
type envelope struct {
	Bytes      *string `json:"bytes"`
	Properties struct {
		PushPortSequence struct {
			String string `json:"string"`
		} `json:"PushPortSequence"`
	} `json:"properties"`
}

// DecodeRecord turns a Kafka record value (or a bare Push Port message) into
// a Pport.
func DecodeRecord(value []byte) (*Pport, error) {
	value = bytes.TrimSpace(value)
	if len(value) > 0 && value[0] == '<' {
		return Parse(value)
	}
	var env envelope
	if err := json.Unmarshal(value, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	if env.Bytes == nil {
		return nil, errors.New("envelope has no bytes field")
	}
	payload := strings.TrimSpace(*env.Bytes)
	switch {
	case strings.HasPrefix(payload, "{"):
		return parseJSON([]byte(payload))
	case strings.HasPrefix(payload, "<"):
		return Parse([]byte(payload))
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("envelope bytes are neither JSON, XML nor base64: %w", err)
	}
	return Parse(raw)
}

// The JSON rendering of Push Port follows the XML closely: attributes and
// child elements both become keys, a repeated element becomes an array while
// a single one is an object, element text sits under the "" key, and every
// value is a string. These types absorb those quirks and convert to the same
// structs the XML parser produces.

type jPport struct {
	TS      string   `json:"ts"`
	Version string   `json:"version"`
	UR      *jUpdate `json:"uR"`
	SR      *jUpdate `json:"sR"`
}

type jUpdate struct {
	UpdateOrigin string             `json:"updateOrigin"`
	TS           many[jTrainStatus] `json:"TS"`
	Schedules    many[jSchedule]    `json:"schedule"`
	Deactivated  many[struct {
		RID string `json:"rid"`
	}] `json:"deactivated"`
	Associations many[jAssociation]    `json:"association"`
	Messages     many[jStationMessage] `json:"OW"`
}

type jAssociation struct {
	TIPLOC    string `json:"tiploc"`
	Category  string `json:"category"`
	Cancelled flag   `json:"isCancelled"`
	Deleted   flag   `json:"isDeleted"`
	Main      struct {
		RID string `json:"rid"`
	} `json:"main"`
	Assoc struct {
		RID string `json:"rid"`
	} `json:"assoc"`
}

type jTrainStatus struct {
	RID        string            `json:"rid"`
	UID        string            `json:"uid"`
	SSD        string            `json:"ssd"`
	LateReason *jText            `json:"LateReason"`
	Locations  many[jTSLocation] `json:"Location"`
}

type jTSLocation struct {
	TPL  string     `json:"tpl"`
	WTA  string     `json:"wta"`
	WTD  string     `json:"wtd"`
	WTP  string     `json:"wtp"`
	PTA  string     `json:"pta"`
	PTD  string     `json:"ptd"`
	Arr  *jForecast `json:"arr"`
	Dep  *jForecast `json:"dep"`
	Pass *jForecast `json:"pass"`
	Plat *jPlat     `json:"plat"`
}

type jForecast struct {
	ET        string `json:"et"`
	WET       string `json:"wet"`
	AT        string `json:"at"`
	ATRemoved flag   `json:"atRemoved"`
	ETUnknown flag   `json:"etUnknown"`
	Delayed   flag   `json:"delayed"`
	Src       string `json:"src"`
}

type jPlat struct {
	Number                    string
	PlatSup, CISPlatSup, Conf bool
}

// UnmarshalJSON reads the platform number from the "" key, which struct
// tags cannot name.
func (p *jPlat) UnmarshalJSON(b []byte) error {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*p = jPlat{Number: str(m[""]), PlatSup: str(m["platsup"]) == "true",
		CISPlatSup: str(m["cisPlatsup"]) == "true", Conf: str(m["conf"]) == "true"}
	return nil
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return ""
}

type jSchedule struct {
	RID          string          `json:"rid"`
	UID          string          `json:"uid"`
	SSD          string          `json:"ssd"`
	TOC          string          `json:"toc"`
	Deleted      flag            `json:"deleted"`
	IsPassenger  *flag           `json:"isPassengerSvc"`
	CancelReason *jText          `json:"cancelReason"`
	OR           many[jSchedLoc] `json:"OR"`
	OPOR         many[jSchedLoc] `json:"OPOR"`
	IP           many[jSchedLoc] `json:"IP"`
	OPIP         many[jSchedLoc] `json:"OPIP"`
	PP           many[jSchedLoc] `json:"PP"`
	DT           many[jSchedLoc] `json:"DT"`
	OPDT         many[jSchedLoc] `json:"OPDT"`
}

type jSchedLoc struct {
	TPL string `json:"tpl"`
	WTA string `json:"wta"`
	WTD string `json:"wtd"`
	WTP string `json:"wtp"`
	Can flag   `json:"can"`
}

// many accepts either a single object or an array of them.
type many[T any] []T

func (m *many[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		return json.Unmarshal(b, (*[]T)(m))
	}
	var one T
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*m = []T{one}
	return nil
}

// flag accepts true, "true" or "false".
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	*f = flag(s == "true")
	return nil
}

// jText is an element with text and optional attributes, which appears as
// either a bare string or an object with the text under "".
type jText struct {
	Text   string
	TIPLOC string
	Near   bool
}

func (t *jText) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		t.Text = s
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*t = jText{Text: str(m[""]), TIPLOC: str(m["tiploc"]), Near: str(m["near"]) == "true"}
	return nil
}

func (t *jText) reason() *Reason {
	if t == nil {
		return nil
	}
	return &Reason{Code: t.Text, TIPLOC: t.TIPLOC, Near: t.Near}
}

func parseJSON(b []byte) (*Pport, error) {
	var j jPport
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("decode push port json: %w", err)
	}
	return &Pport{TS: j.TS, Version: j.Version, UR: j.UR.convert(), SR: j.SR.convert()}, nil
}

func (u *jUpdate) convert() *Update {
	if u == nil {
		return nil
	}
	out := &Update{UpdateOrigin: u.UpdateOrigin}
	for _, ts := range u.TS {
		t := TrainStatus{RID: ts.RID, UID: ts.UID, SSD: ts.SSD, LateReason: ts.LateReason.reason()}
		for _, l := range ts.Locations {
			loc := TSLocation{TPL: l.TPL, WTA: l.WTA, WTD: l.WTD, WTP: l.WTP, PTA: l.PTA, PTD: l.PTD,
				Arr: l.Arr.convert(), Dep: l.Dep.convert(), Pass: l.Pass.convert()}
			if l.Plat != nil {
				loc.Plat = &Plat{Number: l.Plat.Number, PlatSup: l.Plat.PlatSup,
					CISPlatSup: l.Plat.CISPlatSup, Conf: l.Plat.Conf}
			}
			t.Locations = append(t.Locations, loc)
		}
		out.TrainStatus = append(out.TrainStatus, t)
	}
	for _, s := range u.Schedules {
		sch := Schedule{RID: s.RID, UID: s.UID, SSD: s.SSD, TOC: s.TOC, Deleted: bool(s.Deleted),
			CancelReason: s.CancelReason.reason()}
		if s.IsPassenger != nil {
			v := bool(*s.IsPassenger)
			sch.IsPassenger = &v
		}
		for name, locs := range map[string]many[jSchedLoc]{"OR": s.OR, "OPOR": s.OPOR, "IP": s.IP,
			"OPIP": s.OPIP, "PP": s.PP, "DT": s.DT, "OPDT": s.OPDT} {
			for _, l := range locs {
				sch.Locations = append(sch.Locations, ScheduleLoc{
					XMLName: xmlName(name), TPL: l.TPL, WTA: l.WTA, WTD: l.WTD, WTP: l.WTP, Can: bool(l.Can)})
			}
		}
		out.Schedules = append(out.Schedules, sch)
	}
	for _, a := range u.Associations {
		var as Association
		as.TIPLOC, as.Category = a.TIPLOC, a.Category
		as.Cancelled, as.Deleted = bool(a.Cancelled), bool(a.Deleted)
		as.Main.RID, as.Assoc.RID = a.Main.RID, a.Assoc.RID
		out.Associations = append(out.Associations, as)
	}
	for _, m := range u.Messages {
		out.Messages = append(out.Messages, m.convert())
	}
	for _, d := range u.Deactivated {
		out.Deactivated = append(out.Deactivated, struct {
			RID string `xml:"rid,attr"`
		}{d.RID})
	}
	return out
}

func xmlName(local string) xml.Name { return xml.Name{Local: local} }

func (f *jForecast) convert() *Forecast {
	if f == nil {
		return nil
	}
	return &Forecast{ET: f.ET, WET: f.WET, AT: f.AT, ATRemoved: bool(f.ATRemoved),
		ETUnknown: bool(f.ETUnknown), Delayed: bool(f.Delayed), Src: f.Src}
}
