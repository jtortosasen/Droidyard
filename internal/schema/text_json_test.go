package schema_test

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestTextSurvivesJSON holds the decision that lets the CLI render a result it received as
// JSON (plan m2z D7): for each descriptor with a Text, rendering a result and rendering the
// same result after a JSON round trip, decoded into a *T as the CLI does, give the same
// text. A renderer that read a field the wire drops, or a type that does not decode back,
// would show a person something else than MCP shows an agent.
func TestTextSurvivesJSON(t *testing.T) {
	seen := 0
	for _, d := range registry().All() {
		if d.Text == nil {
			continue
		}
		seen++
		for variant := 0; variant < variants; variant++ {
			v := filled(d, variant)
			want := d.Text(v.Interface())
			if want == "" {
				t.Fatalf("%s: the rendering of a filled result is empty", d.Name)
			}
			raw, err := json.Marshal(v.Interface())
			if err != nil {
				t.Fatalf("%s: %v", d.Name, err)
			}
			back := reflect.New(reflect.TypeOf(d.Result))
			if err := json.Unmarshal(raw, back.Interface()); err != nil {
				t.Fatalf("%s: the result does not decode from its own JSON: %v", d.Name, err)
			}
			if got := d.Text(back.Interface()); got != want {
				t.Errorf("%s (variant %d): the rendering differs after a JSON round trip\nbefore:\n%s\nafter:\n%s", d.Name, variant, want, got)
			}
		}
	}
	if seen != len(rendered) {
		t.Errorf("checked %d renderings, want %d", seen, len(rendered))
	}
}
