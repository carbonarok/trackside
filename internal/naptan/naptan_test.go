package naptan

import (
	"strings"
	"testing"
)

const sample = "\xef\xbb\xbfATCOCode,NaptanCode,CommonName,Longitude,Latitude,StopType,Status\n" +
	"9100RDNGSTN,,Reading Rail Station,-0.97184879131,51.45878601669,RLY,active\n" +
	"9100OLDSTN,,Closed Rail Station,-1.0,51.0,RLY,inactive\n" +
	"9100RDNG4AB,,Reading Rail Station,-0.9718,51.4587,RSE,active\n" +
	"0100BRP90312,,A bus stop,-2.6,51.4,BCT,active\n"

func TestParse(t *testing.T) {
	got, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("stations = %+v, want only Reading", got)
	}
	r := got[0]
	if r.TIPLOC != "RDNGSTN" || r.Name != "Reading" || r.Lat < 51.45 || r.Lat > 51.46 || r.Lon > -0.97 {
		t.Errorf("Reading = %+v", r)
	}
}
