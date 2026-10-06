package trust

import (
	_ "embed"
	"encoding/json"
)

// delayCodes is Section S of the Delay Attribution Principles and Rules: the
// codes TRUST uses for delay and cancellation causes.
//
//go:embed delay_codes.json
var delayCodesJSON []byte

// DelayCode describes a TRUST delay attribution code.
type DelayCode struct {
	Cause        string `json:"cause"`
	Abbreviation string `json:"abbreviation"`
}

var delayCodes = func() map[string]DelayCode {
	var doc struct {
		Codes map[string]DelayCode `json:"codes"`
	}
	if err := json.Unmarshal(delayCodesJSON, &doc); err != nil {
		panic("trust: bad delay_codes.json: " + err.Error())
	}
	return doc.Codes
}()

// LookupDelayCode returns the description of a delay attribution code such
// as "IA" (signal failure).
func LookupDelayCode(code string) (DelayCode, bool) {
	c, ok := delayCodes[code]
	return c, ok
}
