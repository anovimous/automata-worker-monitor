package workerapi

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestParseRefusesAnotherSchemaVersion(t *testing.T) {
	for _, doc := range []string{`{"schemaVersion": 2, "workerId": "w"}`, `{"workerId": "w"}`} {
		_, err := Parse([]byte(doc))
		if !errors.Is(err, ErrUnsupportedSchema) {
			t.Errorf("Parse(%s) error = %v, want ErrUnsupportedSchema", doc, err)
		}
	}
}

func TestParseRefusesInvalidJSON(t *testing.T) {
	for _, doc := range []string{`{`, `[]`, `{"schemaVersion": 1, "pid": "seven"}`} {
		_, err := Parse([]byte(doc))
		if err == nil || errors.Is(err, ErrUnsupportedSchema) {
			t.Errorf("Parse(%s) error = %v, want a decoding error", doc, err)
		}
	}
}

func TestParseIgnoresUnknownFields(t *testing.T) {
	s, err := Parse([]byte(`{"schemaVersion": 1, "workerId": "w-1", "addedLater": {"x": 1}}`))
	if err != nil {
		t.Fatalf("Parse returned an error: %v", err)
	}
	if s.WorkerID != "w-1" {
		t.Errorf("WorkerID = %q, want w-1", s.WorkerID)
	}
}

func TestProgressAgeUsesTheDocumentsOwnClock(t *testing.T) {
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	s := Status{Now: now, LastProgressAt: now.Add(-1602 * time.Second)}
	if got := s.ProgressAge(); got != 1602*time.Second {
		t.Errorf("ProgressAge() = %s, want 1602s", got)
	}
}

func TestOptionalObjectsAreWrittenAsNull(t *testing.T) {
	b, err := json.Marshal(Status{})
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"job", "wordlistCache"} {
		v, ok := doc[key]
		if !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", key, v, ok)
		}
	}
}
