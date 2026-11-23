package search

import "testing"

// TestFold verifies a term is folded to lower case and nothing else. Trimming
// here would silently widen a search the caller wrote deliberately.
func TestFold(t *testing.T) {
	cases := map[string]string{
		"ifInOctets": "ifinoctets",
		"IF-MIB":     "if-mib",
		"":           "",
		"  spaced  ": "  spaced  ",
	}
	for raw, want := range cases {
		if got := Fold(raw); got != want {
			t.Errorf("Fold(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestContains verifies case-insensitive substring matching over several
// fields, and that an empty needle selects everything.
func TestContains(t *testing.T) {
	tests := []struct {
		name   string
		needle string
		values []string
		want   bool
	}{
		{"matches first field", "ifin", []string{"ifInOctets", "1.3.6"}, true},
		{"matches later field", "1.3.6", []string{"ifInOctets", "1.3.6"}, true},
		{"folds the value", "octets", []string{"ifInOCTETS"}, true},
		{"no match", "zzz", []string{"ifInOctets", "1.3.6"}, false},
		{"empty needle selects everything", "", []string{"anything"}, true},
		{"empty needle with no values", "", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Contains(tc.needle, tc.values...); got != tc.want {
				t.Errorf("Contains(%q, %v) = %v, want %v", tc.needle, tc.values, got, tc.want)
			}
		})
	}
}

// TestContains_UnfoldedNeedle documents the one way a caller can misuse this:
// a needle that was not folded matches nothing, because the values are folded
// and the needle is not. Fold is not applied here because it would then run
// once per record instead of once per query.
func TestContains_UnfoldedNeedle(t *testing.T) {
	if Contains("IfIn", "ifInOctets") {
		t.Error("an unfolded needle matched; callers must pass Fold(term)")
	}
	if !Contains(Fold("IfIn"), "ifInOctets") {
		t.Error("a folded needle did not match")
	}
}

// TestObject verifies an object is searched by name, OID and description, and
// by nothing else.
func TestObject(t *testing.T) {
	const (
		name = "ifInOctets"
		oid  = "1.3.6.1.2.1.2.2.1.10"
		desc = "The total number of octets received on the interface."
	)

	tests := []struct {
		needle string
		want   bool
	}{
		{"ifinoctets", true},
		{"inoct", true},
		{"1.3.6.1.2.1.2.2.1.10", true},
		{"2.2.1.10", true},
		{"octets received", true},
		{"", true},
		{"iftable", false},
		{"9.9.9", false},
	}
	for _, tc := range tests {
		if got := Object(tc.needle, name, oid, desc); got != tc.want {
			t.Errorf("Object(%q) = %v, want %v", tc.needle, got, tc.want)
		}
	}
}

// TestObject_EmptyFields verifies an object with no text is only selected by an
// empty search, rather than matching or panicking on anything.
func TestObject_EmptyFields(t *testing.T) {
	if !Object("", "", "", "") {
		t.Error("an empty search must select even an empty object")
	}
	if Object("anything", "", "", "") {
		t.Error("an empty object matched a non-empty search")
	}
}

// TestModule verifies a module is searched by name, description and tags.
func TestModule(t *testing.T) {
	tags := []string{"cisco", "routing"}

	tests := []struct {
		needle string
		want   bool
	}{
		{"cisco-bgp", true},
		{"border gateway", true},
		{"cisco", true},
		{"routing", true},
		{"", true},
		{"juniper", false},
	}
	for _, tc := range tests {
		got := Module(tc.needle, "CISCO-BGP4-MIB", "Border Gateway Protocol", tags)
		if got != tc.want {
			t.Errorf("Module(%q) = %v, want %v", tc.needle, got, tc.want)
		}
	}
}

// TestModule_NoTags verifies a module with no tags is searched by its name and
// description alone rather than being excluded.
func TestModule_NoTags(t *testing.T) {
	if !Module("if-mib", "IF-MIB", "Interfaces", nil) {
		t.Error("a module with no tags was not matched by its name")
	}
	if Module("cisco", "IF-MIB", "Interfaces", nil) {
		t.Error("a module with no tags matched a tag search")
	}
}

// TestHasTag verifies tag selection ignores case and that no tag selects
// everything.
func TestHasTag(t *testing.T) {
	tags := []string{"Cisco", "routing"}

	if !HasTag(tags, "cisco") {
		t.Error("tag match must ignore case")
	}
	if !HasTag(tags, "") {
		t.Error("an empty tag must select every module")
	}
	if HasTag(tags, "juniper") {
		t.Error("an absent tag matched")
	}
	if HasTag(nil, "cisco") {
		t.Error("a module with no tags matched a tag")
	}
	if !HasTag(nil, "") {
		t.Error("an empty tag must select a module with no tags")
	}
}
