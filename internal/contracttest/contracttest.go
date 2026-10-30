// Package contracttest helps the contract tests read the samples in contracts/ and compare JSON documents.
package contracttest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// Dir is the contracts/ directory of the repo, found from this file so that tests work from any package.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "contracts")
}

// Read returns the content of a sample.
func Read(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(Dir(), name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// DecodeStrict unmarshals b into v and fails on a field that v lacks, so that a sample field missing from a type
// is caught.
func DecodeStrict(t testing.TB, b []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decoding into %T: %v", v, err)
	}
}

// SameJSON reports whether two documents hold the same values, whatever their formatting and key order.
func SameJSON(a, b []byte) (bool, error) {
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		return false, err
	}
	return reflect.DeepEqual(va, vb), nil
}

// AssertRoundTrip decodes a sample strictly into v, encodes v again and fails when the result differs from the
// sample: a field renamed, dropped or written as omitted instead of null shows up here.
func AssertRoundTrip(t testing.TB, name string, v any) {
	t.Helper()
	sample := Read(t, name)
	DecodeStrict(t, sample, v)
	got, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	same, err := SameJSON(got, sample)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Errorf("%s does not round-trip through %T:\ngot  %s", name, v, got)
	}
}
