package darwin

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reference is Darwin's reference data file (*_ref_v*.xml).
type Reference struct {
	Locations []struct {
		TPL     string `xml:"tpl,attr"`
		CRS     string `xml:"crs,attr"`
		LocName string `xml:"locname,attr"`
	} `xml:"LocationRef"`
	TOCs []struct {
		TOC  string `xml:"toc,attr"`
		Name string `xml:"tocname,attr"`
		URL  string `xml:"url,attr"`
	} `xml:"TocRef"`
	LateReasons   []refReason `xml:"LateRunningReasons>Reason"`
	CancelReasons []refReason `xml:"CancellationReasons>Reason"`
}

type refReason struct {
	Code int    `xml:"code,attr"`
	Text string `xml:"reasontext,attr"`
}

// ParseReference decodes a reference data file.
func ParseReference(r io.Reader) (*Reference, error) {
	var ref Reference
	if err := xml.NewDecoder(r).Decode(&ref); err != nil {
		return nil, fmt.Errorf("decode darwin reference: %w", err)
	}
	return &ref, nil
}

// LoadReference stores reason texts and operator names, and replaces the
// upper-case industry names of passenger stations with Darwin's public names
// ("London Waterloo" rather than "LONDON WATERLOO").
func LoadReference(ctx context.Context, pool *pgxpool.Pool, ref *Reference) error {
	b := &pgx.Batch{}
	for _, r := range ref.LateReasons {
		b.Queue(`INSERT INTO darwin_reasons (kind, code, text) VALUES ('late', $1, $2)
			ON CONFLICT (kind, code) DO UPDATE SET text = EXCLUDED.text`, r.Code, r.Text)
	}
	for _, r := range ref.CancelReasons {
		b.Queue(`INSERT INTO darwin_reasons (kind, code, text) VALUES ('cancel', $1, $2)
			ON CONFLICT (kind, code) DO UPDATE SET text = EXCLUDED.text`, r.Code, r.Text)
	}
	for _, t := range ref.TOCs {
		b.Queue(`INSERT INTO operators (code, name, url) VALUES ($1, $2, $3)
			ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name, url = EXCLUDED.url`,
			t.TOC, t.Name, nullEmpty(t.URL))
	}
	for _, l := range ref.Locations {
		// Only stations have a CRS; other locnames are just the TIPLOC again.
		if l.CRS == "" || l.LocName == "" || strings.EqualFold(l.LocName, l.TPL) {
			continue
		}
		// CORPUS gives a big station's CRS to one of its TIPLOCs only; Darwin
		// gives it to all of them (Clapham Junction has five), which is what
		// a station board needs to find every train.
		b.Queue(`UPDATE locations SET name = $2, crs = $3, name_source = 'darwin' WHERE tiploc = $1`,
			l.TPL, l.LocName, strings.ToUpper(l.CRS))
	}
	return pool.SendBatch(ctx, b).Close()
}
