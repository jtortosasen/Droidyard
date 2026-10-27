package capability

import (
	"reflect"
	"slices"
	"testing"
)

type embeddedFilter struct {
	Match   []string `json:"match,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type embeddingParams struct {
	Device string `json:"device"`
	embeddedFilter
	Limit int `json:"limit,omitempty"`
}

// Decision (plan m3i step 3, N2): a params struct may embed another, and the schema and
// encoding/json flatten it. The names a REQUEST_INVALID_PARAMS hint and a type error list
// are then the flattened ones, in declaration order, never the embedding type's Go name.
func TestParamNamesAndShapeFlattenEmbedded(t *testing.T) {
	if got, want := paramNames(embeddingParams{}), []string{"device", "match", "exclude", "limit"}; !slices.Equal(got, want) {
		t.Errorf("paramNames = %v; want %v", got, want)
	}
	if got, want := jsonShape(reflect.TypeOf(embeddingParams{})), "an object {device, match?, exclude?, limit?}"; got != want {
		t.Errorf("jsonShape = %q; want %q", got, want)
	}
}
