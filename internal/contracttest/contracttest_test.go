package contracttest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirHoldsTheSamples(t *testing.T) {
	if _, err := os.Stat(filepath.Join(Dir(), "worker-status.v1.running.json")); err != nil {
		t.Errorf("Dir() = %s, the running sample is not there: %v", Dir(), err)
	}
}

func TestReadReturnsTheSample(t *testing.T) {
	if b := Read(t, "worker-status.v1.idle.json"); len(b) == 0 {
		t.Error("Read returned an empty sample")
	}
}

func TestDecodeStrictAcceptsKnownFields(t *testing.T) {
	var v struct {
		A int `json:"a"`
	}
	DecodeStrict(t, []byte(`{"a": 1}`), &v)
	if v.A != 1 {
		t.Errorf("A = %d, want 1", v.A)
	}
}

func TestDecodeStrictRefusesUnknownFields(t *testing.T) {
	failed := fails(t, func(tb testing.TB) {
		var v struct {
			A int `json:"a"`
		}
		DecodeStrict(tb, []byte(`{"a": 1, "b": 2}`), &v)
	})
	if !failed {
		t.Error("DecodeStrict accepted the unknown field b")
	}
}

func TestSameJSON(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[1,2]}`, "{\n  \"b\": [1, 2],\n  \"a\": 1\n}", true},
		{`{"a":1}`, `{"a":2}`, false},
		{`{"a":null}`, `{}`, false},
		{`{"a":[]}`, `{"a":null}`, false},
	}
	for _, c := range cases {
		got, err := SameJSON([]byte(c.a), []byte(c.b))
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("SameJSON(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestSameJSONRejectsInvalidJSON(t *testing.T) {
	if _, err := SameJSON([]byte(`{`), []byte(`{}`)); err == nil {
		t.Error("SameJSON returned no error for invalid JSON")
	}
}

func TestAssertRoundTripPassesForAMatchingType(t *testing.T) {
	failed := fails(t, func(tb testing.TB) {
		var v map[string]any
		AssertRoundTrip(tb, "worker-status.v1.idle.json", &v)
	})
	if failed {
		t.Error("AssertRoundTrip failed for a map, which keeps every field")
	}
}

func TestAssertRoundTripFailsForATypeThatLacksFields(t *testing.T) {
	failed := fails(t, func(tb testing.TB) {
		var v struct {
			SchemaVersion int `json:"schemaVersion"`
		}
		AssertRoundTrip(tb, "worker-status.v1.idle.json", &v)
	})
	if !failed {
		t.Error("AssertRoundTrip passed for a type that lacks most fields")
	}
}

// fails runs fn with a testing.TB that records a failure instead of failing t, and reports whether fn failed.
func fails(t *testing.T, fn func(tb testing.TB)) (failed bool) {
	rec := &recordingTB{TB: t}
	defer func() {
		if r := recover(); r != nil && r != errFatal {
			panic(r)
		}
		failed = rec.failed
	}()
	fn(rec)
	return rec.failed
}

var errFatal = new(int)

type recordingTB struct {
	testing.TB
	failed bool
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Errorf(string, ...any) { r.failed = true }

// Fatal and Fatalf stop fn like testing does; fails recovers.
func (r *recordingTB) Fatal(...any) { r.failed = true; panic(errFatal) }

func (r *recordingTB) Fatalf(string, ...any) { r.failed = true; panic(errFatal) }
