package build

import (
	"encoding/json"
	"strings"
	"testing"
)

// Decision (step 3 review B2): a record written before 0.7.0 has no expectedSources, and
// its result omits the key rather than publish empty values outside the enum.
func TestRecordWithoutExpectedSourcesOmitsTheKey(t *testing.T) {
	var rec record
	if err := json.Unmarshal([]byte(`{"buildId":"op-0000000000000001","outcome":"success"}`), &rec); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(rec.result())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "expectedSources") {
		t.Fatalf("an old record publishes expectedSources: %s", out)
	}
}
