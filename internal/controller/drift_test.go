package controller

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFieldHashes_IgnoresKeyOrderAndWhitespace(t *testing.T) {
	a, err := fieldHashes([]byte(`{"a":{"x":1,"y":2},"b":[1]}`), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := fieldHashes([]byte(`{ "b":[1], "a":{"y":2,"x":1} }`), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("expected equal hashes, got %v vs %v", a, b)
	}
}

func TestFieldHashes_AbsentKeyHashesAsNull(t *testing.T) {
	got, err := fieldHashes([]byte(`{"a":1}`), []string{"c"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := fieldHashes([]byte(`{"c":null}`), []string{"c"})
	if err != nil {
		t.Fatal(err)
	}
	if got["c"] != want["c"] {
		t.Fatalf("absent %q != null %q", got["c"], want["c"])
	}
	if len(got["c"]) != 16 {
		t.Fatalf("expected 16 chars, got %d", len(got["c"]))
	}
}

func TestFieldHashes_IgnoresUnmanagedKeys(t *testing.T) {
	keys := []string{"a", "b"}
	base, err := fieldHashes([]byte(`{"a":1,"b":2}`), keys)
	if err != nil {
		t.Fatal(err)
	}
	with, err := fieldHashes([]byte(`{"a":1,"b":2,"pauseUntil":"2026-01-01"}`), keys)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base, with) {
		t.Fatalf("unmanaged key changed hashes: %v vs %v", base, with)
	}
}

func TestDriftedFields(t *testing.T) {
	baseline := map[string]string{"a": "1", "b": "2"}
	if got := driftedFields(baseline, map[string]string{"a": "1", "b": "3"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("got %v", got)
	}
	if got := driftedFields(baseline, map[string]string{"a": "1", "b": "2"}); len(got) != 0 {
		t.Fatalf("expected none, got %v", got)
	}
	if got := driftedFields(baseline, map[string]string{"a": "1"}); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("missing key: got %v", got)
	}
}

func TestPayloadKeys(t *testing.T) {
	got, err := payloadKeys([]byte(`{"tags":[],"name":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"name", "tags"}) {
		t.Fatalf("got %v", got)
	}
}

func TestHoldsDesired(t *testing.T) {
	decode := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	desired := decode(`[{"priority":2,"configuration":{"burnRate":14.4}}]`)
	for remote, want := range map[string]bool{
		`[{"id":"a","sloId":"s","priority":2,"configuration":{"burnRate":14.4}}]`: true,
		`[{"id":"a","priority":3,"configuration":{"burnRate":14.4}}]`:             false,
		`[{"priority":2,"configuration":{}}]`:                                     false,
		`[]`:                                                                      false,
		`null`:                                                                    false,
	} {
		if got := holdsDesired(decode(remote), desired); got != want {
			t.Errorf("holdsDesired(%s)=%v, want %v", remote, got, want)
		}
	}
}
