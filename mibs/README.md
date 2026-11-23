# MIB Repository

This directory contains MIB (Management Information Base) files used by go-netmon for SNMP monitoring.

## Directory Structure

```
mibs/
├── standard/     Standard IETF and industry MIBs
├── cisco/        Cisco-specific MIBs (requires manual download)
└── vendor/       Other vendor-specific MIBs
```

## Downloaded MIBs

### Standard MIBs (standard/)

| MIB File | OIDs | Description | Status |
|----------|------|-------------|--------|
| SNMPv2-SMI.txt | - | SMI base definitions | ✅ |
| SNMPv2-TC.txt | - | Textual conventions | ✅ |
| SNMPv2-CONF.txt | - | Conformance definitions | ✅ |
| SNMPv2-MIB.txt | 70 | System group (sysDescr, sysUpTime, etc.) | ✅ Tested |
| IF-MIB.txt | 91 | Interface statistics (ifTable, ifXTable) | ✅ Tested |
| IP-MIB.txt | - | IP protocol statistics | ✅ |
| TCP-MIB.txt | - | TCP protocol statistics | ✅ |
| UDP-MIB.txt | - | UDP protocol statistics | ✅ |
| IANAifType-MIB.txt | - | Interface type definitions | ✅ |
| IF-INVERTED-STACK-MIB.txt | - | Interface stacking | ✅ |

**Total**: 10 MIB files, 161+ OIDs

### Integration Test Results

Parser successfully tested with:
- **SNMPv2-MIB**: 70 OIDs loaded, sysDescr validated
- **IF-MIB**: 91 OIDs loaded, 5 tables found, 43 pollable OIDs identified
  - ifTable: 22 columns, 1 index
  - Successfully extracts table structure and metadata

## Usage

### Loading MIBs

```go
import (
    "github.com/jeremyhahn/go-netmon/internal/mib/parser"
    "github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

// Initialize storage
store, _ := storage.New("./data/mibs")
defer store.Close()

// Initialize parser
parser := parser.New("/path/to/mibs/standard")
parser.Init()
defer parser.Close()

// Load and parse a MIB
oids, _ := parser.ParseModule("IF-MIB")
for _, oid := range oids {
    store.StoreOID(oid)
}
```

### Querying Pollable OIDs

```go
// Get all pollable OIDs for a module
pollable, _ := store.GetPollableOIDs("IF-MIB")

// Filter by criteria
for _, oid := range pollable {
    if oid.IsCounter() {
        fmt.Printf("Counter: %s (%s)\n", oid.Name, oid.OID)
    }
}
```

## Adding New MIBs

1. Download MIB file (.txt or .my format)
2. Place in appropriate directory (standard/, cisco/, or vendor/)
3. Ensure dependencies are available
4. Load using parser or mib-loader tool

### Dependencies

Many MIBs depend on base MIBs. Ensure you have:
- SNMPv2-SMI, SNMPv2-TC, SNMPv2-CONF (already included)
- Module-specific imports (check MIB IMPORTS section)

## Source

Standard MIBs downloaded from: http://www.net-snmp.org/mibs/

For vendor MIBs, see vendor-specific READMEs:
- [Cisco MIBs](cisco/README.md)

## Testing

Run parser tests with MIBs:

```bash
go test ./internal/mib/parser/
```

## License

The MIB files in this directory are not covered by the project's Apache-2.0
license. Each file stays under the copyright and terms stated in its own
header:

- `standard/`: IETF and IANA MIBs, published under the IETF Trust's terms for
  MIB modules, which permit redistribution.
- `asr1001/`, `cisco/`: Cisco Systems MIBs, copyright Cisco Systems, Inc.
- `r640/`: Dell, Broadcom and QLogic MIBs, copyright their respective vendors.
- `vendor/`: ATEN and Raritan MIBs, copyright their respective vendors.

The vendor MIBs are included because the parser tests and `make validate-mibs`
need them, and because each vendor publishes them for use with its devices.
If you are a rights holder and want a file removed, open an issue.
