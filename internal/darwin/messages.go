package darwin

import (
	"context"
	"encoding/json"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// StationMessage (OW) is a notice for one or more stations.
type StationMessage struct {
	ID       string `xml:"id,attr"`
	Category string `xml:"cat,attr"`
	Severity string `xml:"sev,attr"`
	Suppress bool   `xml:"suppress,attr"`
	Stations []struct {
		CRS string `xml:"crs,attr"`
	} `xml:"Station"`
	Msg struct {
		Inner string `xml:",innerxml"`
	} `xml:"Msg"`
}

var (
	tagRE   = regexp.MustCompile(`<[^>]+>`)
	spaceRE = regexp.MustCompile(`\s+`)
	// Push Port namespaces the p and a elements; drop the prefixes so the
	// stored body is plain HTML.
	nsPrefixRE = regexp.MustCompile(`<(/?)[A-Za-z0-9]+:`)
	nsAttrRE   = regexp.MustCompile(`\s+xmlns(:[A-Za-z0-9]+)?="[^"]*"`)
)

// messageBody returns the message as simple HTML and as plain text.
func messageBody(inner string) (htmlBody, text string) {
	htmlBody = strings.TrimSpace(nsAttrRE.ReplaceAllString(nsPrefixRE.ReplaceAllString(inner, "<$1"), ""))
	text = html.UnescapeString(tagRE.ReplaceAllString(strings.ReplaceAll(htmlBody, "</p>", "</p> "), ""))
	return htmlBody, strings.TrimSpace(spaceRE.ReplaceAllString(text, " "))
}

// applyMessage stores or removes a station message.
func (a *Applier) applyMessage(ctx context.Context, m *StationMessage) error {
	id, err := strconv.Atoi(strings.TrimSpace(m.ID))
	if err != nil {
		return nil
	}
	var stations []string
	for _, s := range m.Stations {
		if crs := strings.ToUpper(strings.TrimSpace(s.CRS)); crs != "" {
			stations = append(stations, crs)
		}
	}
	if len(stations) == 0 {
		_, err := a.Pool.Exec(ctx, `DELETE FROM station_messages WHERE id = $1`, id)
		return err
	}
	sev, _ := strconv.Atoi(m.Severity)
	htmlBody, text := messageBody(m.Msg.Inner)
	_, err = a.Pool.Exec(ctx, `INSERT INTO station_messages (id, category, severity, suppress, html, text, stations)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO UPDATE SET category = EXCLUDED.category, severity = EXCLUDED.severity,
			suppress = EXCLUDED.suppress, html = EXCLUDED.html, text = EXCLUDED.text,
			stations = EXCLUDED.stations, updated_at = now()`,
		id, m.Category, sev, m.Suppress, htmlBody, text, stations)
	return err
}

// jStationMessage is the JSON rendering of OW. The message body's layout in
// JSON isn't documented, so its text is gathered from whatever strings it
// contains and links are kept as anchors.
type jStationMessage struct {
	ID       string `json:"id"`
	Category string `json:"cat"`
	Severity string `json:"sev"`
	Suppress flag   `json:"suppress"`
	Stations many[struct {
		CRS string `json:"crs"`
	}] `json:"Station"`
	Msg json.RawMessage `json:"Msg"`
}

func (j jStationMessage) convert() StationMessage {
	m := StationMessage{ID: j.ID, Category: j.Category, Severity: j.Severity, Suppress: bool(j.Suppress)}
	for _, s := range j.Stations {
		m.Stations = append(m.Stations, struct {
			CRS string `xml:"crs,attr"`
		}{s.CRS})
	}
	var v any
	if json.Unmarshal(j.Msg, &v) == nil {
		var b strings.Builder
		renderJSONMsg(&b, v)
		m.Msg.Inner = b.String()
	}
	return m
}

// renderJSONMsg rebuilds simple HTML from the JSON form of a message body.
func renderJSONMsg(b *strings.Builder, v any) {
	switch x := v.(type) {
	case string:
		b.WriteString(html.EscapeString(x))
	case []any:
		for _, e := range x {
			renderJSONMsg(b, e)
		}
	case map[string]any:
		if href, ok := x["href"].(string); ok {
			b.WriteString(`<a href="` + html.EscapeString(href) + `">`)
			renderJSONMsg(b, x[""])
			b.WriteString(`</a>`)
			return
		}
		if t, ok := x[""]; ok {
			renderJSONMsg(b, t)
		}
		if p, ok := x["p"]; ok {
			ps, isList := p.([]any)
			if !isList {
				ps = []any{p}
			}
			for _, e := range ps {
				b.WriteString("<p>")
				renderJSONMsg(b, e)
				b.WriteString("</p>")
			}
		}
		if a, ok := x["a"]; ok {
			// JSON loses the whitespace between text and a link.
			if s := b.String(); s != "" && !strings.HasSuffix(s, " ") && !strings.HasSuffix(s, ">") {
				b.WriteString(" ")
			}
			renderJSONMsg(b, a)
		}
	}
}
