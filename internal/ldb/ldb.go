// Package ldb is a client for National Rail's Darwin Lite web service
// (OpenLDBWS, the Live Departure Boards Web Service). It answers one station
// board or one service at a time, so it is used on demand, with caching and a
// request budget, by instances that don't have the Darwin Push Port stream.
package ldb

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/carbonarok/trackside/internal/timetable"
)

// DefaultURL is the 2021-11-01 version of the service.
const DefaultURL = "https://lite.realtime.nationalrail.co.uk/OpenLDBWS/ldb12.asmx"

const (
	// DefaultHourlyBudget stays under the free tier's 5,000 requests an hour.
	DefaultHourlyBudget = 4500
	boardTTL            = time.Minute
	serviceTTL          = time.Minute
	bindingTTL          = 4 * time.Hour
	messageTTL          = 10 * time.Minute
	// The service answers for -120 to +119 minutes around now, at most 120
	// minutes wide.
	maxOffsetBack  = 120
	maxOffsetAhead = 119
	maxWindow      = 120
	maxRows        = 150
)

// ErrBudget means the hourly request budget is spent.
var ErrBudget = errors.New("darwin lite hourly request budget spent")

// Client implements timetable.LiveSource against Darwin Lite.
type Client struct {
	Token        string
	URL          string
	HTTP         *http.Client
	HourlyBudget int
	// Now is the clock; tests override it.
	Now func() time.Time

	mu       sync.Mutex
	hour     time.Time
	used     int
	boards   map[string]cached[*timetable.LiveBoard]
	services map[string]cached[*timetable.LiveService]
	bindings map[string]cached[string]
	messages map[string]cached[[]timetable.Message]
}

type cached[T any] struct {
	value   T
	expires time.Time
}

// New returns a client for a developer token.
func New(token string) *Client {
	return &Client{Token: token}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

var _ timetable.LiveSource = (*Client)(nil)

var crsRE = regexp.MustCompile(`^[A-Z]{3}$`)

// Board fetches the station's combined arrivals and departures board for
// the part of [from, to) the service can answer.
func (c *Client) Board(ctx context.Context, crs string, from, to time.Time) (*timetable.LiveBoard, error) {
	crs = strings.ToUpper(crs)
	if !crsRE.MatchString(crs) {
		return nil, nil
	}
	now := c.now()
	offset := int(from.Sub(now).Minutes())
	end := int(to.Sub(now).Minutes())
	if end <= -maxOffsetBack || offset > maxOffsetAhead {
		return nil, nil // entirely outside what the service covers
	}
	offset = max(offset, -maxOffsetBack)
	// Round the start down to 5 minutes so nearby requests share a cache
	// entry.
	offset -= ((offset % 5) + 5) % 5
	offset = max(offset, -maxOffsetBack)
	window := min(max(end-offset, 1), maxWindow)
	key := fmt.Sprintf("%s/%d/%d", crs, offset, window)

	c.mu.Lock()
	if b, ok := c.boards[key]; ok && now.Before(b.expires) {
		c.mu.Unlock()
		return b.value, nil
	}
	c.mu.Unlock()

	var res envelope
	err := c.call(ctx, "GetArrivalDepartureBoard", fmt.Sprintf(
		`<ldb:GetArrivalDepartureBoardRequest><ldb:numRows>%d</ldb:numRows><ldb:crs>%s</ldb:crs>`+
			`<ldb:timeOffset>%d</ldb:timeOffset><ldb:timeWindow>%d</ldb:timeWindow></ldb:GetArrivalDepartureBoardRequest>`,
		maxRows, crs, offset, window), &res)
	if err != nil {
		return nil, err
	}
	r := res.Body.Board
	if r == nil {
		return nil, errors.New("darwin lite: no board in response")
	}
	board := &timetable.LiveBoard{}
	for _, list := range [][]service{r.Trains, r.Buses, r.Ferries} {
		for _, s := range list {
			board.Services = append(board.Services, s.live())
		}
	}
	msgs := make([]timetable.Message, 0, len(r.Messages))
	for _, m := range r.Messages {
		htmlBody, text := messageBody(m)
		msgs = append(msgs, timetable.Message{Category: "Station", Severity: 1, Text: text,
			HTML: htmlBody, UpdatedAt: now})
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.boards == nil {
		c.boards = map[string]cached[*timetable.LiveBoard]{}
		c.messages = map[string]cached[[]timetable.Message]{}
	}
	c.boards[key] = cached[*timetable.LiveBoard]{board, now.Add(boardTTL)}
	c.messages[crs] = cached[[]timetable.Message]{msgs, now.Add(messageTTL)}
	c.sweep(now)
	return board, nil
}

// Bind remembers which Darwin Lite service ID a timetable service has.
func (c *Client) Bind(serviceID, uid string, runDate time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bindings == nil {
		c.bindings = map[string]cached[string]{}
	}
	c.bindings[bindKey(uid, runDate)] = cached[string]{serviceID, c.now().Add(bindingTTL)}
}

func bindKey(uid string, runDate time.Time) string {
	return uid + "/" + runDate.Format(time.DateOnly)
}

// Service fetches a service's calling points, if a board has bound it.
func (c *Client) Service(ctx context.Context, uid string, runDate time.Time) (*timetable.LiveService, error) {
	now := c.now()
	c.mu.Lock()
	b, ok := c.bindings[bindKey(uid, runDate)]
	if !ok || now.After(b.expires) {
		c.mu.Unlock()
		return nil, nil
	}
	id := b.value
	if s, ok := c.services[id]; ok && now.Before(s.expires) {
		c.mu.Unlock()
		return s.value, nil
	}
	c.mu.Unlock()

	var res envelope
	if err := c.call(ctx, "GetServiceDetails", `<ldb:GetServiceDetailsRequest><ldb:serviceID>`+
		html.EscapeString(id)+`</ldb:serviceID></ldb:GetServiceDetailsRequest>`, &res); err != nil {
		return nil, err
	}
	d := res.Body.Details
	if d == nil {
		return nil, nil // service IDs expire once the train has gone
	}
	svc := d.live()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.services == nil {
		c.services = map[string]cached[*timetable.LiveService]{}
	}
	c.services[id] = cached[*timetable.LiveService]{svc, now.Add(serviceTTL)}
	return svc, nil
}

// Messages returns the station messages from the station's latest board.
func (c *Client) Messages(crs string) []timetable.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.messages[strings.ToUpper(crs)]
	if !ok || c.now().After(m.expires) {
		return nil
	}
	return m.value
}

// sweep drops expired cache entries. Callers hold mu.
func (c *Client) sweep(now time.Time) {
	for k, v := range c.boards {
		if now.After(v.expires) {
			delete(c.boards, k)
		}
	}
	for k, v := range c.services {
		if now.After(v.expires) {
			delete(c.services, k)
		}
	}
	for k, v := range c.bindings {
		if now.After(v.expires) {
			delete(c.bindings, k)
		}
	}
}

// spend takes one request from the hourly budget.
func (c *Client) spend() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	hour := c.now().Truncate(time.Hour)
	if !hour.Equal(c.hour) {
		c.hour, c.used = hour, 0
	}
	limit := c.HourlyBudget
	if limit == 0 {
		limit = DefaultHourlyBudget
	}
	if c.used >= limit {
		return false
	}
	c.used++
	return true
}

func (c *Client) call(ctx context.Context, action, body string, out *envelope) error {
	if !c.spend() {
		return ErrBudget
	}
	url := c.URL
	if url == "" {
		url = DefaultURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(
		`<?xml version="1.0" encoding="utf-8"?>`+
			`<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"`+
			` xmlns:typ="http://thalesgroup.com/RTTI/2013-11-28/Token/types"`+
			` xmlns:ldb="http://thalesgroup.com/RTTI/2021-11-01/ldb/">`+
			`<soap:Header><typ:AccessToken><typ:TokenValue>`+html.EscapeString(c.Token)+
			`</typ:TokenValue></typ:AccessToken></soap:Header>`+
			`<soap:Body>`+body+`</soap:Body></soap:Envelope>`))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"http://thalesgroup.com/RTTI/2012-01-13/ldb/`+action+`"`)
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("darwin lite %s: %w", action, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("darwin lite %s: %w", action, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("darwin lite %s: token rejected (check NRE_LDBWS_TOKEN)", action)
	}
	if err := xml.Unmarshal(b, out); err != nil {
		return fmt.Errorf("darwin lite %s: %s: %w", action, resp.Status, err)
	}
	if f := out.Body.Fault; f != nil {
		return fmt.Errorf("darwin lite %s: %s", action, f.String)
	}
	return nil
}

// Response structures, matched by local name so the many versioned
// namespace prefixes don't matter.

type envelope struct {
	Body struct {
		Fault *struct {
			String string `xml:"faultstring"`
		} `xml:"Fault"`
		Board   *boardResult   `xml:"GetArrivalDepartureBoardResponse>GetStationBoardResult"`
		Details *detailsResult `xml:"GetServiceDetailsResponse>GetServiceDetailsResult"`
	} `xml:"Body"`
}

type boardResult struct {
	Messages []string  `xml:"nrccMessages>message"`
	Trains   []service `xml:"trainServices>service"`
	Buses    []service `xml:"busServices>service"`
	Ferries  []service `xml:"ferryServices>service"`
}

type location struct {
	CRS string `xml:"crs"`
}

type service struct {
	STA          string     `xml:"sta"`
	ETA          string     `xml:"eta"`
	STD          string     `xml:"std"`
	ETD          string     `xml:"etd"`
	Platform     string     `xml:"platform"`
	OperatorCode string     `xml:"operatorCode"`
	Cancelled    bool       `xml:"isCancelled"`
	CancelReason string     `xml:"cancelReason"`
	DelayReason  string     `xml:"delayReason"`
	ServiceID    string     `xml:"serviceID"`
	Origin       []location `xml:"origin>location"`
	Destination  []location `xml:"destination>location"`
}

func (s service) live() timetable.LiveBoardService {
	l := timetable.LiveBoardService{
		ServiceID: s.ServiceID, STA: s.STA, ETA: s.ETA, STD: s.STD, ETD: s.ETD,
		Platform: s.Platform, OperatorCode: s.OperatorCode, Cancelled: s.Cancelled,
		CancelReason: s.CancelReason, DelayReason: s.DelayReason,
	}
	for _, o := range s.Origin {
		l.OriginCRS = append(l.OriginCRS, o.CRS)
	}
	for _, d := range s.Destination {
		l.DestinationCRS = append(l.DestinationCRS, d.CRS)
	}
	return l
}

type callingPoint struct {
	CRS       string `xml:"crs"`
	ST        string `xml:"st"`
	ET        string `xml:"et"`
	AT        string `xml:"at"`
	Cancelled bool   `xml:"isCancelled"`
}

type detailsResult struct {
	CRS          string `xml:"crs"`
	STA          string `xml:"sta"`
	ETA          string `xml:"eta"`
	ATA          string `xml:"ata"`
	STD          string `xml:"std"`
	ETD          string `xml:"etd"`
	ATD          string `xml:"atd"`
	Cancelled    bool   `xml:"isCancelled"`
	CancelReason string `xml:"cancelReason"`
	DelayReason  string `xml:"delayReason"`
	// A train that divides has one list per portion.
	Previous   []callingPointList `xml:"previousCallingPoints>callingPointList"`
	Subsequent []callingPointList `xml:"subsequentCallingPoints>callingPointList"`
}

type callingPointList struct {
	Points []callingPoint `xml:"callingPoint"`
}

func (d *detailsResult) live() *timetable.LiveService {
	s := &timetable.LiveService{CancelReason: d.CancelReason, DelayReason: d.DelayReason}
	add := func(cp callingPoint) {
		s.Calls = append(s.Calls, timetable.LiveCall{CRS: cp.CRS, ST: cp.ST, ET: cp.ET, AT: cp.AT, Cancelled: cp.Cancelled})
	}
	for _, list := range d.Previous {
		for _, cp := range list.Points {
			add(cp)
		}
	}
	// The station the board was fetched for.
	if d.STA != "" {
		add(callingPoint{CRS: d.CRS, ST: d.STA, ET: d.ETA, AT: d.ATA, Cancelled: d.Cancelled})
	}
	if d.STD != "" {
		add(callingPoint{CRS: d.CRS, ST: d.STD, ET: d.ETD, AT: d.ATD, Cancelled: d.Cancelled})
	}
	for _, list := range d.Subsequent {
		for _, cp := range list.Points {
			add(cp)
		}
	}
	return s
}

var (
	tagRE   = regexp.MustCompile(`<[^>]+>`)
	spaceRE = regexp.MustCompile(`\s+`)
)

// messageBody turns a station message, which may contain simple HTML, into
// HTML and plain text.
func messageBody(m string) (htmlBody, text string) {
	htmlBody = strings.TrimSpace(m)
	text = html.UnescapeString(tagRE.ReplaceAllString(htmlBody, ""))
	return htmlBody, strings.TrimSpace(spaceRE.ReplaceAllString(text, " "))
}
