package parser

import (
	"testing"

	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/testsupport"
)

// allMIBDirs is every MIB directory in the repository. Parsing against the full
// set is what production does, and it is the only configuration in which a
// module's imports all resolve.
func allMIBDirs() []string {
	return testsupport.MIBDirs("standard", "cisco", "asr1001", "vendor", "r640")
}

// parseWith parses module with the given directories on the search path and
// returns its objects. It reports a parse error as a failure; the point of
// several tests here is that an under-resolved parse does *not* produce one.
func parseWith(t *testing.T, module string, dirs ...string) []*mib.OIDMetadata {
	t.Helper()

	p := New(dirs...)
	if err := p.Init(); err != nil {
		t.Fatalf("init parser: %v", err)
	}
	// gosmi keeps its loaded modules in package state, so the parser is closed
	// before this returns. Deferring it to the end of the test would let a
	// second parse in the same test reuse the first one's cache and silently
	// answer for the wrong search path.
	defer p.Close()

	oids, err := p.ParseModule(module)
	if err != nil {
		t.Fatalf("parse %s: %v", module, err)
	}
	return oids
}

// unitsByObject collects the objects that declare a UNITS clause.
func unitsByObject(oids []*mib.OIDMetadata) map[string]string {
	units := make(map[string]string)
	for _, oid := range oids {
		if oid.Units != "" {
			units[oid.Name] = oid.Units
		}
	}
	return units
}

// untypedAccessible names the readable objects that came back with no type.
func untypedAccessible(oids []*mib.OIDMetadata) []string {
	var untyped []string
	for _, oid := range oids {
		if oid.Type == "" && (oid.Access == "read-only" || oid.Access == "read-write") {
			untyped = append(untyped, oid.Name)
		}
	}
	return untyped
}

// TestUnitsSurviveAcrossAWholeModule pins the UNITS clause of every IP-MIB object that declares one.
func TestUnitsSurviveAcrossAWholeModule(t *testing.T) {
	oids := parseWith(t, "IP-MIB", allMIBDirs()...)

	want := map[string]string{
		"ipReasmTimeout":                      "seconds",
		"ipv4InterfaceRetransmitTime":         "milliseconds",
		"ipv6InterfaceReasmMaxSize":           "octets",
		"ipv6InterfaceReachableTime":          "milliseconds",
		"ipv6InterfaceRetransmitTime":         "milliseconds",
		"ipSystemStatsRefreshRate":            "milli-seconds",
		"ipIfStatsRefreshRate":                "milli-seconds",
		"ipAddressPrefixAdvPreferredLifetime": "seconds",
		"ipAddressPrefixAdvValidLifetime":     "seconds",
		"ipDefaultRouterLifetime":             "seconds",
		"ipv6RouterAdvertMaxInterval":         "seconds",
		"ipv6RouterAdvertMinInterval":         "seconds",
		"ipv6RouterAdvertReachableTime":       "milliseconds",
		"ipv6RouterAdvertRetransmitTime":      "milliseconds",
		"ipv6RouterAdvertDefaultLifetime":     "seconds",
	}

	got := unitsByObject(oids)

	for name, wantUnit := range want {
		gotUnit, ok := got[name]
		if !ok {
			t.Errorf("%s lost its UNITS clause", name)
			continue
		}
		if gotUnit != wantUnit {
			t.Errorf("%s Units = %q, want %q", name, gotUnit, wantUnit)
		}
	}
	for name, gotUnit := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("%s gained a UNITS clause of %q that IP-MIB does not declare", name, gotUnit)
		}
	}
}

// TestUnitsSurviveInTCPMIB is the same guarantee on a second module, so a
// regression cannot be mistaken for something specific to IP-MIB.
func TestUnitsSurviveInTCPMIB(t *testing.T) {
	oids := parseWith(t, "TCP-MIB", testsupport.MIBDir("standard"))

	want := map[string]string{
		"tcpRtoMin": "milliseconds",
		"tcpRtoMax": "milliseconds",
	}

	got := unitsByObject(oids)
	for name, wantUnit := range want {
		if got[name] != wantUnit {
			t.Errorf("%s Units = %q, want %q", name, got[name], wantUnit)
		}
	}
}

// TestFullSearchPathResolvesTypesAndHints checks IP-MIB objects resolve concrete types and DISPLAY-HINTs.
func TestFullSearchPathResolvesTypesAndHints(t *testing.T) {
	oids := parseWith(t, "IP-MIB", allMIBDirs()...)

	if len(oids) != 293 {
		t.Errorf("IP-MIB yielded %d objects, want 293", len(oids))
	}

	byName := make(map[string]*mib.OIDMetadata, len(oids))
	for _, oid := range oids {
		byName[oid.Name] = oid
	}

	tests := []struct {
		object       string
		wantType     string
		wantHint     string
		wantPollable bool
	}{
		// An inline INTEGER enumeration resolves to type Enumeration, labels in EnumValues.
		{object: "ipForwarding", wantType: "Enumeration", wantPollable: true},
		{object: "ipDefaultTTL", wantType: "Integer32", wantPollable: true},
		{object: "ipReasmTimeout", wantType: "Integer32", wantPollable: true},
		{object: "ipSystemStatsInReceives", wantType: "Counter32", wantPollable: true},
		{object: "ipAddressPrefixOrigin", wantType: "IpAddressPrefixOriginTC", wantPollable: true},
		{object: "ipNetToPhysicalPhysAddress", wantType: "PhysAddress", wantHint: "1x:", wantPollable: true},
		// A table node is not itself pollable; its columns are.
		{object: "ipSystemStatsTable", wantPollable: false},
	}

	for _, tt := range tests {
		t.Run(tt.object, func(t *testing.T) {
			oid, ok := byName[tt.object]
			if !ok {
				t.Fatalf("%s is missing from the parsed module", tt.object)
			}
			if tt.wantType != "" && oid.Type != tt.wantType {
				t.Errorf("Type = %q, want %q", oid.Type, tt.wantType)
			}
			if tt.wantHint != "" && oid.DisplayHint != tt.wantHint {
				t.Errorf("DisplayHint = %q, want %q", oid.DisplayHint, tt.wantHint)
			}
			if oid.Pollable != tt.wantPollable {
				t.Errorf("Pollable = %v, want %v", oid.Pollable, tt.wantPollable)
			}
		})
	}
}

// TestAnIncompleteSearchPathIsReported checks an incomplete search path yields no usable objects and reports the drop count.
func TestAnIncompleteSearchPathIsReported(t *testing.T) {
	// IP-MIB's imports are not in the asr1001 directory.
	partial, partialReport := parseReportWith(t, "IP-MIB", testsupport.MIBDir("asr1001"))
	full, fullReport := parseReportWith(t, "IP-MIB", allMIBDirs()...)

	if len(full) == 0 {
		t.Fatal("the full parse yielded nothing; the fixture is broken")
	}
	if fullReport.UnresolvedCount() != 0 {
		t.Errorf("a complete search path still discarded %d objects: %v",
			fullReport.UnresolvedCount(), fullReport.Unresolved)
	}
	if units := unitsByObject(full); len(units) == 0 {
		t.Error("the full parse recovered no UNITS clauses")
	}

	// Nothing usable comes out of the partial parse, because nothing it defines
	// resolved to an OID.
	for _, object := range partial {
		if object.OID == "" {
			t.Errorf("%s came back with no OID", object.Name)
		}
	}
	if len(partial) >= len(full) {
		t.Fatalf("the partial parse yielded %d objects and the full one %d; the fixture no longer demonstrates the degradation",
			len(partial), len(full))
	}

	// And the loss is stated rather than left to be inferred from thin metadata.
	if partialReport.UnresolvedCount() == 0 {
		t.Error("an incomplete search path discarded nothing and reported nothing; " +
			"the degradation is silent again")
	}
}

// parseReportWith parses moduleName with dirs on the search path and returns
// both the objects and what was discarded.
func parseReportWith(t *testing.T, moduleName string, dirs ...string) ([]*mib.OIDMetadata, *ParseReport) {
	t.Helper()

	p := New(dirs...)
	if err := p.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(p.Close)

	objects, report, err := p.ParseModuleWithReport(moduleName)
	if err != nil {
		t.Fatalf("parsing %s: %v", moduleName, err)
	}
	return objects, report
}

// TestInetAddressMIBResolvesIPMIBZoneColumns checks INET-ADDRESS-MIB (RFC 4001) in mibs/standard types IP-MIB's InetZoneIndex columns.
func TestInetAddressMIBResolvesIPMIBZoneColumns(t *testing.T) {
	oids := parseWith(t, "IP-MIB", allMIBDirs()...)

	byName := make(map[string]*mib.OIDMetadata, len(oids))
	for _, oid := range oids {
		byName[oid.Name] = oid
	}

	zone, ok := byName["ipv6ScopeZoneIndexLinkLocal"]
	if !ok {
		t.Fatal("ipv6ScopeZoneIndexLinkLocal is missing from the parsed module")
	}
	if zone.Type == "" {
		t.Error("ipv6ScopeZoneIndexLinkLocal has no type: INET-ADDRESS-MIB is not resolving, so this readable column would be polled untyped")
	}

	// Every readable object in the module must now carry a type.
	if untyped := untypedAccessible(oids); len(untyped) != 0 {
		t.Errorf("%d readable IP-MIB objects have no type: %v", len(untyped), untyped)
	}
}
