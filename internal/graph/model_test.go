package graph_test

import (
	"reflect"
	"testing"

	"github.com/TheFellow/weave/internal/graph"
)

func TestTokensSplitIdentifiersDeterministically(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		want []string
	}{
		{name: "HandleHTTPRequest2", want: []string{"2", "handle", "http", "request"}},
		{name: "parse_user-ID", want: []string{"id", "parse", "user"}},
		{name: "HTTPServerHTTP", want: []string{"http", "server"}},
	} {
		if got := graph.Tokens(test.name); !reflect.DeepEqual(got, test.want) {
			t.Errorf("Tokens(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestUnitFactsValidationRejectsCrossUnitDocument(t *testing.T) {
	t.Parallel()
	facts := graph.UnitFacts{
		Unit:      graph.Unit{ID: "a", Provider: "fixture", ProviderVersion: "1"},
		Documents: []graph.Document{{ID: "doc", UnitID: "b", Path: "a.go", Provider: "fixture"}},
	}
	if err := facts.Validate(); err == nil {
		t.Fatal("Validate() error = nil")
	}
}
