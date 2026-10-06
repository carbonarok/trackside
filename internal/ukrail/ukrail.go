// Package ukrail holds small helpers shared across the feed parsers and API:
// UK local time, rail time formats, operator names and location naming.
package ukrail

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// London is the Europe/London zone. Every time in the timetable feeds is UK
// local wall-clock time.
var London = mustLoad("Europe/London")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// ParseWTT parses a CIF working time "HHMM" with an optional trailing "H"
// (half minute) into seconds after midnight. ok is false for blank input.
func ParseWTT(s string) (secs int, ok bool) {
	s = strings.TrimSpace(s)
	half := strings.HasSuffix(s, "H")
	s = strings.TrimSuffix(s, "H")
	if len(s) != 4 {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[2:])
	if err1 != nil || err2 != nil || h > 23 || m > 59 {
		return 0, false
	}
	secs = h*3600 + m*60
	if half {
		secs += 30
	}
	return secs, true
}

// ParseVSTPTime parses a VSTP time "HHMMSS" into seconds after midnight.
func ParseVSTPTime(s string) (secs int, ok bool) {
	s = strings.TrimSpace(s)
	if len(s) != 6 {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[2:4])
	sec, err3 := strconv.Atoi(s[4:])
	if err1 != nil || err2 != nil || err3 != nil || h > 23 || m > 59 || sec > 59 {
		return 0, false
	}
	return h*3600 + m*60 + sec, true
}

// AtRunDate converts seconds-after-midnight on a run date into an absolute
// UK local time. Offsets of 86400 or more fall on following days.
func AtRunDate(runDate time.Time, secs int) time.Time {
	y, m, d := runDate.Date()
	return time.Date(y, m, d, 0, 0, secs, 0, London)
}

// HHMM formats seconds after midnight as "HHMM", wrapping past midnight.
func HHMM(secs int) string {
	secs %= 86400
	return pad2(secs/3600) + pad2(secs%3600/60)
}

// HHMMSS formats seconds after midnight as "HHMMSS", wrapping past midnight.
func HHMMSS(secs int) string {
	secs %= 86400
	return pad2(secs/3600) + pad2(secs%3600/60) + pad2(secs%60)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// DisplayName turns an upper-case industry description such as
// "LONDON WATERLOO" into "London Waterloo".
func DisplayName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	upperNext := true
	for _, r := range strings.ToLower(s) {
		if upperNext && unicode.IsLetter(r) {
			b.WriteRune(unicode.ToUpper(r))
			upperNext = false
			continue
		}
		b.WriteRune(r)
		upperNext = r == ' ' || r == '-' || r == '(' || r == '/' || r == '.' || r == '&'
	}
	return b.String()
}

// Operators maps ATOC business codes to operator names.
var Operators = map[string]string{
	"AW": "Transport for Wales",
	"CC": "c2c",
	"CH": "Chiltern Railways",
	"CS": "Caledonian Sleeper",
	"EM": "East Midlands Railway",
	"ES": "Eurostar",
	"GC": "Grand Central",
	"GN": "Great Northern",
	"GR": "LNER",
	"GW": "Great Western Railway",
	"GX": "Gatwick Express",
	"HT": "Hull Trains",
	"HX": "Heathrow Express",
	"IL": "Island Line",
	"LD": "Lumo",
	"LE": "Greater Anglia",
	"LM": "West Midlands Trains",
	"LO": "London Overground",
	"LT": "London Underground",
	"ME": "Merseyrail",
	"NT": "Northern",
	"SE": "Southeastern",
	"SN": "Southern",
	"SR": "ScotRail",
	"SW": "South Western Railway",
	"TL": "Thameslink",
	"TP": "TransPennine Express",
	"TW": "Tyne and Wear Metro",
	"VT": "Avanti West Coast",
	"XC": "CrossCountry",
	"XR": "Elizabeth line",
}

// OperatorName returns the operator's name, or the code itself if unknown.
func OperatorName(code string) string {
	if name, ok := Operators[code]; ok {
		return name
	}
	return code
}

// IsPassenger reports whether a CIF train status/category describes a
// passenger-carrying train.
func IsPassenger(trainStatus, category string) bool {
	switch trainStatus {
	case "P", "1":
		switch category {
		case "OO", "OL", "OU", "OS", "OW", "XC", "XD", "XI", "XR", "XU", "XX", "XZ", "BR", "BS":
			return true
		}
	case "B", "5":
		return true
	}
	return false
}

// Today returns the current UK calendar date as midnight UTC, the form used
// for Postgres date values throughout.
func Today() time.Time {
	return DateOf(time.Now())
}

// DateOf returns t's UK calendar date as midnight UTC.
func DateOf(t time.Time) time.Time {
	y, m, d := t.In(London).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
