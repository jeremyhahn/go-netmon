# MIB Importer Tool

The MIB Importer is a command-line tool for downloading Cisco MIBs from the [cisco-mibs GitHub repository](https://github.com/cisco/cisco-mibs) based on device support lists.

## Features

- Parse Cisco support list HTML files to extract MIB names
- Download MIBs concurrently with progress tracking
- Organize MIBs by device model
- Handle missing MIBs gracefully
- Skip already downloaded files
- Comprehensive error reporting and statistics

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

Example:
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
- `--base-url <url>`: Base URL for downloads (default: Cisco GitHub)
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

Output:
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

### Interactive Mode

Run the tool interactively:

```bash
./bin/mib-importer import
```

The tool will prompt for:
1. Path to support list HTML file
2. Device name
3. Confirmation before downloading

## File Organization

MIBs are organized by device in the `mibs/` directory:

```
mibs/
├── standard/          # Standard MIBs (existing)
├── cisco/            # Shared Cisco MIBs (existing)
├── asr1001/          # ASR 1001 specific MIBs
│   ├── CISCO-SYSTEM-MIB.my
│   ├── CISCO-MEMORY-POOL-MIB.my
│   └── missing-mibs.txt
├── nexus9000/        # Nexus 9000 specific MIBs
│   ├── CISCO-NX-OS-MIB.my
│   └── missing-mibs.txt
└── ...
```

## HTML Support List Format

The tool parses Cisco support list HTML files that contain MIB names in:
- Table cells (`<td>`)
- List items (`<li>`)

The parser handles various MIB name formats:
- `CISCO-SYSTEM-MIB`
- `cisco-system-mib` (normalized to uppercase)
- `CISCO-SYSTEM-MIB.my` (extension removed)
- `IF-MIB (v2)` (version info removed)

## Error Handling

The tool gracefully handles:
- Missing or inaccessible MIB files (tracked in `missing-mibs.txt`)
- Network errors (retried for different filename patterns)
- Already existing files (skipped unless `--overwrite` is used)
- Invalid HTML files (error reported)

## Performance

- Concurrent downloads: Up to 10 by default (configurable)
- Timeout: 30 seconds per MIB
- Progress updates: Real-time display (can be disabled)
- Average throughput: ~15-20 MIBs/second

## Architecture

### Components

1. **Parser** (`internal/mib/importer/parser.go`)
   - HTML parsing using `golang.org/x/net/html`
   - MIB name extraction and normalization
   - Deduplication

2. **Downloader** (`internal/mib/importer/downloader.go`)
   - Concurrent downloads with semaphore-based limiting
   - Multiple filename pattern attempts
   - Atomic counters for thread-safe statistics
   - Temporary file handling for safe writes

3. **CLI** (`cmd/mib-importer/main.go`)
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

Current coverage: 78.9%

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

## Future Enhancements

Potential improvements:
- Support for other MIB sources (IETF, vendor-specific)
- MIB dependency resolution
- Validation of downloaded MIBs
- Checksum verification
- Resume interrupted downloads
- Batch processing of multiple support lists
- Web UI for easier usage
