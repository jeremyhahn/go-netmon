# MIB Importer Tool

> **Status: REFERENCE** · Scope: go-netmon · Last verified: 2026-10-09

`mib-importer` is a command-line tool with two jobs: downloading Cisco MIBs
from the [cisco-mibs GitHub repository](https://github.com/cisco/cisco-mibs)
based on device support lists (this page), and generating Grafana dashboards
from stored MIBs (the `dashboard` subcommand, see
[dashboard-generation.md](dashboard-generation.md)).

## Features

- Parse Cisco support list HTML files to extract MIB names
- Download MIBs concurrently with progress tracking
- Organize MIBs by device model
- Record MIBs that are not in the repository
- Skip already downloaded files
- Report download statistics

## Installation

Build the tool using make:

```bash
make build-mib-importer
```

The binary will be created at `./bin/mib-importer`.

## Usage

### Parse Command

Extract and display MIB names from a support list HTML file:

```bash
./bin/mib-importer parse <support-list-file>
```

Example (the count depends on the file):
```bash
./bin/mib-importer parse ./support-lists/asr1001-supportlist.html
```

Output:
```
Parsing support list: ./support-lists/asr1001-supportlist.html
Found 170 MIBs:

  CISCO-SYSTEM-MIB
  CISCO-MEMORY-POOL-MIB
  IF-MIB
  ...
```

The names are printed in no particular order.

### Download Command

Download MIBs for a specific device:

```bash
./bin/mib-importer download \
  --device asr1001 \
  --support-list ./support-lists/asr1001-supportlist.html
```

Options:
- `--device <name>`: Device name (required) - used for output directory
- `--support-list <file>`: Path to HTML support list file (required)
- `--base-url <url>`: Base URL for downloads (default:
  `https://raw.githubusercontent.com/cisco/cisco-mibs/main/v2`)
- `--output <dir>`: Custom output directory (default: `./mibs/<device>`)
- `--overwrite`: Overwrite existing MIB files
- `--concurrency <n>`: Maximum concurrent downloads (default: 10)
- `--no-progress`: Disable progress updates

Example with all options:
```bash
./bin/mib-importer download \
  --device nexus9000 \
  --support-list ./support-lists/nexus9000-supportlist.html \
  --output ./mibs/nexus9000 \
  --concurrency 20 \
  --overwrite
```

Output (illustrative numbers):
```
Parsing support list: ./support-lists/nexus9000-supportlist.html
Found 203 MIBs

Downloading MIBs to: ./mibs/nexus9000
Progress: 203/203 (Downloaded: 185, Exists: 0, Not Found: 18, Failed: 0)

=== Download Complete ===
Total MIBs:       203
Downloaded:       185
Already Exists:   0
Not Found:        18
Failed:           0
Success Rate:     91.1%
Duration:         12.3s

Missing MIBs saved to: ./mibs/nexus9000/missing-mibs.txt
```

Success Rate is `(Downloaded + Already Exists) / Total`. Downloads that fail
for a reason other than "not found" are listed under `Failed downloads:` after
the summary. The command still exits 0 when some MIBs are missing or failed;
read the summary.

### Interactive Mode

Run the tool interactively:

```bash
./bin/mib-importer import
```

The tool will prompt for:
1. Path to support list HTML file
2. Device name
3. Confirmation before downloading

Interactive mode always uses the default base URL, `./mibs/<device>`, a
concurrency of 10, and never overwrites existing files. It does not list failed
downloads.

### Dashboard Command

`mib-importer dashboard` (`generate <module>`, `generate-all`, `list`,
`clean`) generates Grafana dashboards; see
[dashboard-generation.md](dashboard-generation.md).

## File Organization

Each device gets its own directory under `mibs/`, next to the existing shared
directories (`standard/`, `cisco/`, `vendor/`):

```
mibs/
├── standard/          # Standard MIBs (existing)
├── cisco/             # Shared Cisco MIBs (existing)
├── asr1001/           # ASR 1001 specific MIBs
│   ├── CISCO-SYSTEM-MIB.my
│   ├── CISCO-MEMORY-POOL-MIB.my
│   └── missing-mibs.txt
├── nexus9000/         # Nexus 9000 specific MIBs
│   ├── CISCO-NX-OS-MIB.my
│   └── missing-mibs.txt
└── ...
```

Every file is saved as `<NAME>.my`, where `<NAME>` is the normalized name from
the support list, even when the repository file was found under a different
name (see below). `missing-mibs.txt` is written only when at least one MIB was
not found.

## HTML Support List Format

The tool parses Cisco support list HTML files that contain MIB names in:
- Table cells (`<td>`)
- List items (`<li>`)

A cell or item is taken as a MIB name when its text is 3 to 100 characters and
contains (case-insensitively) one of `CISCO-`, `-MIB`, `-CAPABILITY`,
`SNMPv2-`, `IF-`, `IP-`, `TCP-`, `UDP-`, `RFC` or `IANAifType`.

The parser handles various MIB name formats:
- `CISCO-SYSTEM-MIB`
- `cisco-system-mib` (normalized to uppercase)
- `CISCO-SYSTEM-MIB.my` (extension removed; `.mib` and `.txt` too)
- `IF-MIB (v2)` (parenthetical and anything after a space removed)

Duplicates are removed. A file with no recognizable MIB names is an error.

## Download Fallbacks and Errors

For each MIB the downloader requests `<base-url>/<NAME>.my`. On an HTTP 404 it
tries one alternative: `<NAME>-MIB.my` if the name does not end in `-MIB`, or
the name without `-MIB` if it does. Any other error (network failure, HTTP
status other than 404) stops the attempts for that MIB and counts it as failed.

The tool also handles:
- Not-found MIBs (counted, and listed in `missing-mibs.txt`)
- Already existing files (skipped unless `--overwrite` is used)
- Invalid or empty HTML files (error reported)

Files are written to `<NAME>.my.tmp` and renamed on success, so an interrupted
download does not leave a truncated `.my` file.

## Performance

- Concurrent downloads: up to 10 by default (`--concurrency`), limited with a
  weighted semaphore
- Timeout: 30 seconds per HTTP request (the `defaultTimeout` constant in
  `cmd/mib-importer/main.go`; there is no flag for it)
- Progress updates: a line refreshed every second (disable with `--no-progress`)

## Architecture

### Components

1. **Parser** (`internal/mib/importer/parser.go`)
   - HTML parsing using `golang.org/x/net/html`
   - MIB name extraction and normalization
   - Deduplication

2. **Downloader** (`internal/mib/importer/downloader.go`)
   - Concurrent downloads with semaphore-based limiting
   - Alternate filename attempts on 404
   - Atomic counters for thread-safe statistics
   - Temporary file handling for safe writes

3. **CLI** (`cmd/mib-importer/main.go`, `cmd/mib-importer/dashboard.go`)
   - Command-line interface with subcommands
   - Flag parsing and validation
   - User interaction and progress display

### Statistics Tracking

The downloader tracks:
- Total MIBs requested
- Successfully downloaded
- Already existing
- Not found (404 errors)
- Failed (other errors)
- Duration
- Success rate

## Testing

Run tests:
```bash
make test-mib-importer
```

Generate coverage report:
```bash
make coverage-mib-importer
```

## Examples

### Download ASR 1001 MIBs
```bash
./bin/mib-importer download \
  --device asr1001 \
  --support-list /path/to/asr1001-supportlist.html
```

### Download Nexus 9000 MIBs with high concurrency
```bash
./bin/mib-importer download \
  --device nexus9000 \
  --support-list /path/to/nexus9000-supportlist.html \
  --concurrency 20
```

### Parse only (no download)
```bash
./bin/mib-importer parse /path/to/supportlist.html
```

### Download, copy into the container, and restart

`make import DEVICE=<name>` downloads with `--concurrency 5` from
`mibs/cisco/support-list/<name>-supportlist.html`, copies `mibs/<name>` into
the `go-netmon` container and restarts it.

## Troubleshooting

### Missing MIBs

If many MIBs are not found (404 errors):
1. Check the `missing-mibs.txt` file in the output directory
2. Verify the MIB names in the support list are correct
3. Check if the MIBs exist in the [cisco-mibs repository](https://github.com/cisco/cisco-mibs)
4. Some proprietary MIBs may not be publicly available

### Network Errors

If downloads fail with network errors:
1. Check your internet connection
2. Verify the base URL is accessible
3. Increase the timeout (modify `defaultTimeout` in code)
4. Reduce concurrency to avoid rate limiting

### Parse Errors

If HTML parsing fails:
1. Verify the HTML file is valid
2. Check that MIB names are in `<td>` or `<li>` elements
3. Ensure the file is a Cisco support list (not a different format)
