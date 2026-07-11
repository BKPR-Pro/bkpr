package main

import "testing"

// metaFlag parses repeated -meta key=value pairs into a bag. A destination reads its own namespaced
// keys off the rule this builds, so the parsing has to keep the key and value exactly as given.
func TestMetaFlagParsesKeyValuePairs(t *testing.T) {
	var m metaFlag
	for _, arg := range []string{"rentapp.lease=31", "channel=etransfer"} {
		if err := m.Set(arg); err != nil {
			t.Fatalf("Set(%q): %v", arg, err)
		}
	}
	if m["rentapp.lease"] != "31" || m["channel"] != "etransfer" {
		t.Errorf("bag = %v, want both pairs", map[string]string(m))
	}
}

// The key is a namespaced identifier and never holds an =, but a value might, so the split is on
// the first = only.
func TestMetaFlagSplitsOnTheFirstEquals(t *testing.T) {
	var m metaFlag
	if err := m.Set("note=a=b"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if m["note"] != "a=b" {
		t.Errorf("value = %q, want the rest after the first =", m["note"])
	}
}

func TestMetaFlagRejectsAPairWithoutEquals(t *testing.T) {
	var m metaFlag
	if err := m.Set("rentapp.lease"); err == nil {
		t.Error("a value with no = should be rejected")
	}
}

func TestMetaFlagRejectsAnEmptyKey(t *testing.T) {
	var m metaFlag
	if err := m.Set("=31"); err == nil {
		t.Error("an empty key should be rejected")
	}
}
