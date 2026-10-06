package api

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type specSchema struct {
	Ref        string                `yaml:"$ref"`
	AllOf      []specSchema          `yaml:"allOf"`
	Properties map[string]specSchema `yaml:"properties"`
}

type spec struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]specSchema `yaml:"schemas"`
	} `yaml:"components"`
}

func loadSpec(t *testing.T) spec {
	t.Helper()
	var s spec
	if err := yaml.Unmarshal(openAPISpec, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// properties flattens a schema's properties, following allOf and $ref.
func (s spec) properties(sc specSchema) map[string]bool {
	out := map[string]bool{}
	if sc.Ref != "" {
		name := strings.TrimPrefix(sc.Ref, "#/components/schemas/")
		return s.properties(s.Components.Schemas[name])
	}
	for _, part := range sc.AllOf {
		for k := range s.properties(part) {
			out[k] = true
		}
	}
	for k := range sc.Properties {
		out[k] = true
	}
	return out
}

// jsonFields lists the JSON field names of a struct, flattening embedded
// structs the way encoding/json does.
func jsonFields(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if f.Anonymous && tag == "" {
			for k := range jsonFields(f.Type) {
				out[k] = true
			}
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestSpecMatchesResponses keeps the OpenAPI schemas and the response structs
// in step: every field must be documented, and every documented field must
// exist.
func TestSpecMatchesResponses(t *testing.T) {
	s := loadSpec(t)
	for name, v := range map[string]any{
		"Location": Location{}, "Operator": Operator{}, "Endpoint": Endpoint{}, "Times": Times{},
		"Platform": Platform{}, "Stop": Stop{}, "ServiceSummary": ServiceSummary{},
		"BoardService": BoardService{}, "Message": Message{}, "Board": Board{}, "Position": Position{},
		"Association": Association{}, "ServiceDetail": ServiceDetail{},
		"TrainRef": TrainRef{}, "DelayRepay": DelayRepay{}, "Compensation": Compensation{},
		"HistoricCall": HistoricCall{}, "ServiceRun": ServiceRun{}, "ServiceHistory": ServiceHistory{},
		"Punctuality": Punctuality{}, "LateTrain": LateTrain{}, "StationStats": StationStats{},
	} {
		sc, ok := s.Components.Schemas[name]
		if !ok {
			t.Errorf("schema %s missing from openapi.yaml", name)
			continue
		}
		documented := s.properties(sc)
		actual := jsonFields(reflect.TypeOf(v))
		for f := range actual {
			if !documented[f] {
				t.Errorf("%s.%s is returned but not documented (documented: %v)", name, f, keys(documented))
			}
		}
		for f := range documented {
			if !actual[f] {
				t.Errorf("%s.%s is documented but not returned", name, f)
			}
		}
	}
}

// TestSpecCoversRoutes checks every registered native route is documented.
func TestSpecCoversRoutes(t *testing.T) {
	s := loadSpec(t)
	mux := http.NewServeMux()
	(&Server{}).Register(mux)
	for _, route := range []string{
		"/v1/locations", "/v1/locations/{code}", "/v1/locations/{code}/departures",
		"/v1/locations/{code}/arrivals", "/v1/locations/{code}/messages", "/v1/services/{uid}/{date}",
		"/v1/delay-repay", "/v1/history/services/{uid}", "/v1/stats/locations/{code}",
	} {
		if _, ok := s.Paths[route]; !ok {
			t.Errorf("route %s not in openapi.yaml", route)
		}
	}
	for path := range s.Paths {
		if path == "/healthz" {
			continue
		}
		req, _ := http.NewRequest(http.MethodGet, strings.NewReplacer("{code}", "CLJ", "{uid}", "W1", "{date}", "2026-01-01").Replace(path), nil)
		if _, pattern := mux.Handler(req); pattern == "" {
			t.Errorf("documented path %s has no handler", path)
		}
	}
}
