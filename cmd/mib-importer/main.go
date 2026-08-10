// Command mib-importer downloads and imports MIB modules into the MIB store and
// generates the Grafana dashboards that go with them.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/mib/importer"
)

const (
	defaultBaseURL = "https://raw.githubusercontent.com/cisco/cisco-mibs/main/v2"
	defaultMIBDir  = "./mibs"
	defaultTimeout = 30 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		printUsage()
		return fmt.Errorf("no command specified")
	}

	command := os.Args[1]

	switch command {
	case "parse":
		return runParse()
	case "download":
		return runDownload()
	case "import":
		return runImport()
	case "dashboard":
		return dashboardCommand(os.Args[2:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", command)
	}
}

func runParse() error {
	if len(os.Args) < 3 {
		return fmt.Errorf("usage: mib-importer parse <support-list-file>")
	}

	htmlFile := os.Args[2]

	fmt.Printf("Parsing support list: %s\n", htmlFile)

	mibs, err := importer.ParseSupportList(htmlFile)
	if err != nil {
		return fmt.Errorf("failed to parse support list: %w", err)
	}

	fmt.Printf("Found %d MIBs:\n\n", len(mibs))
	for _, mib := range mibs {
		fmt.Printf("  %s\n", mib)
	}

	return nil
}

func runDownload() error {
	fs := flag.NewFlagSet("download", flag.ExitOnError)

	device := fs.String("device", "", "Device name (e.g., asr1001, nexus9000)")
	supportList := fs.String("support-list", "", "Path to support list HTML file")
	baseURL := fs.String("base-url", defaultBaseURL, "Base URL for MIB downloads")
	outputDir := fs.String("output", "", "Output directory (default: ./mibs/<device>)")
	overwrite := fs.Bool("overwrite", false, "Overwrite existing MIB files")
	concurrency := fs.Int("concurrency", 10, "Maximum concurrent downloads")
	noProgress := fs.Bool("no-progress", false, "Disable progress updates")

	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	if *device == "" {
		return fmt.Errorf("--device flag is required")
	}

	if *supportList == "" {
		return fmt.Errorf("--support-list flag is required")
	}

	// Determine output directory
	outDir := *outputDir
	if outDir == "" {
		outDir = filepath.Join(defaultMIBDir, *device)
	}

	fmt.Printf("Parsing support list: %s\n", *supportList)
	mibs, err := importer.ParseSupportList(*supportList)
	if err != nil {
		return fmt.Errorf("failed to parse support list: %w", err)
	}

	fmt.Printf("Found %d MIBs\n\n", len(mibs))
	fmt.Printf("Downloading MIBs to: %s\n", outDir)

	opts := &importer.DownloadOptions{
		OutputDir:       outDir,
		BaseURL:         *baseURL,
		Overwrite:       *overwrite,
		MaxConcurrency:  *concurrency,
		Timeout:         defaultTimeout,
		ProgressUpdates: !*noProgress,
	}

	downloader := importer.NewDownloader(*baseURL, defaultTimeout)
	ctx := context.Background()

	stats, err := downloader.DownloadAll(ctx, mibs, opts)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Print final statistics
	fmt.Println()
	fmt.Println("=== Download Complete ===")
	fmt.Printf("Total MIBs:       %d\n", stats.TotalMIBs)
	fmt.Printf("Downloaded:       %d\n", stats.Downloaded)
	fmt.Printf("Already Exists:   %d\n", stats.AlreadyExists)
	fmt.Printf("Not Found:        %d\n", stats.NotFound)
	fmt.Printf("Failed:           %d\n", stats.Failed)
	fmt.Printf("Success Rate:     %.1f%%\n", stats.SuccessRate())
	fmt.Printf("Duration:         %s\n", stats.Duration)

	// Save not found MIBs to file
	if len(stats.NotFoundMIBs) > 0 {
		notFoundFile := filepath.Join(outDir, "missing-mibs.txt")
		if err := saveMissingMIBs(notFoundFile, stats.NotFoundMIBs); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save missing MIBs list: %v\n", err)
		} else {
			fmt.Printf("\nMissing MIBs saved to: %s\n", notFoundFile)
		}
	}

	// Print failed MIBs
	if len(stats.FailedMIBs) > 0 {
		fmt.Println("\nFailed downloads:")
		for _, failed := range stats.FailedMIBs {
			fmt.Printf("  %s\n", failed)
		}
	}

	return nil
}

func runImport() error {
	fmt.Println("=== MIB Importer - Interactive Mode ===")
	fmt.Println()

	// Ask for support list file
	fmt.Print("Enter path to support list HTML file: ")
	var supportListPath string
	if _, err := fmt.Scanln(&supportListPath); err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	// Ask for device name
	fmt.Print("Enter device name (e.g., asr1001, nexus9000): ")
	var deviceName string
	if _, err := fmt.Scanln(&deviceName); err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	// Confirm
	outputDir := filepath.Join(defaultMIBDir, deviceName)
	fmt.Printf("\nMIBs will be downloaded to: %s\n", outputDir)
	fmt.Print("Continue? (y/n): ")
	var confirm string
	if _, err := fmt.Scanln(&confirm); err != nil {
		return fmt.Errorf("failed to read input: %w", err)
	}

	if confirm != "y" && confirm != "Y" {
		fmt.Println("Canceled.")
		return nil
	}

	// Parse support list
	fmt.Printf("\nParsing support list: %s\n", supportListPath)
	mibs, err := importer.ParseSupportList(supportListPath)
	if err != nil {
		return fmt.Errorf("failed to parse support list: %w", err)
	}

	fmt.Printf("Found %d MIBs\n\n", len(mibs))
	fmt.Printf("Downloading MIBs to: %s\n", outputDir)

	opts := &importer.DownloadOptions{
		OutputDir:       outputDir,
		BaseURL:         defaultBaseURL,
		Overwrite:       false,
		MaxConcurrency:  10,
		Timeout:         defaultTimeout,
		ProgressUpdates: true,
	}

	downloader := importer.NewDownloader(defaultBaseURL, defaultTimeout)
	ctx := context.Background()

	stats, err := downloader.DownloadAll(ctx, mibs, opts)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Print final statistics
	fmt.Println()
	fmt.Println("=== Download Complete ===")
	fmt.Printf("Total MIBs:       %d\n", stats.TotalMIBs)
	fmt.Printf("Downloaded:       %d\n", stats.Downloaded)
	fmt.Printf("Already Exists:   %d\n", stats.AlreadyExists)
	fmt.Printf("Not Found:        %d\n", stats.NotFound)
	fmt.Printf("Failed:           %d\n", stats.Failed)
	fmt.Printf("Success Rate:     %.1f%%\n", stats.SuccessRate())
	fmt.Printf("Duration:         %s\n", stats.Duration)

	// Save not found MIBs to file
	if len(stats.NotFoundMIBs) > 0 {
		notFoundFile := filepath.Join(outputDir, "missing-mibs.txt")
		if err := saveMissingMIBs(notFoundFile, stats.NotFoundMIBs); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save missing MIBs list: %v\n", err)
		} else {
			fmt.Printf("\nMissing MIBs saved to: %s\n", notFoundFile)
		}
	}

	return nil
}

func saveMissingMIBs(filename string, mibs []string) (err error) {
	f, err := os.Create(filename) // #nosec G304 -- filename is the output path the operator asked for
	if err != nil {
		return err
	}
	// The file is being written, so a failed close means the list on disk is
	// incomplete. Report it unless an earlier error already explains why.
	defer func() {
		if closeErr := f.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	for _, mib := range mibs {
		if _, writeErr := fmt.Fprintln(f, mib); writeErr != nil {
			return writeErr
		}
	}

	return nil
}

func printUsage() {
	fmt.Println("MIB Importer - Download Cisco MIBs and Generate Grafana Dashboards")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  mib-importer <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  parse <file>           Parse a support list HTML file and display MIB names")
	fmt.Println("  download               Download MIBs for a device")
	fmt.Println("  import                 Interactive mode for MIB download")
	fmt.Println("  dashboard              Generate Grafana dashboards from MIBs")
	fmt.Println("  help                   Show this help message")
	fmt.Println()
	fmt.Println("Download Options:")
	fmt.Println("  --device <name>        Device name (required)")
	fmt.Println("  --support-list <file>  Path to support list HTML file (required)")
	fmt.Println("  --base-url <url>       Base URL for downloads (default: Cisco GitHub)")
	fmt.Println("  --output <dir>         Output directory (default: ./mibs/<device>)")
	fmt.Println("  --overwrite            Overwrite existing files")
	fmt.Println("  --concurrency <n>      Max concurrent downloads (default: 10)")
	fmt.Println("  --no-progress          Disable progress updates")
	fmt.Println()
	fmt.Println("Dashboard Subcommands:")
	fmt.Println("  generate <module>      Generate dashboard for specific MIB module")
	fmt.Println("  generate-all           Generate dashboards for all loaded modules")
	fmt.Println("  list                   List available MIB modules")
	fmt.Println("  clean                  Remove all generated dashboards")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  # Parse a support list")
	fmt.Println("  mib-importer parse asr1001-supportlist.html")
	fmt.Println()
	fmt.Println("  # Download MIBs for a device")
	fmt.Println("  mib-importer download --device asr1001 --support-list asr1001-supportlist.html")
	fmt.Println()
	fmt.Println("  # Interactive mode")
	fmt.Println("  mib-importer import")
	fmt.Println()
	fmt.Println("  # Generate dashboard for IF-MIB")
	fmt.Println("  mib-importer dashboard generate IF-MIB")
	fmt.Println()
	fmt.Println("  # Generate all dashboards")
	fmt.Println("  mib-importer dashboard generate-all --output-dir ./grafana/dashboards")
	fmt.Println()
	fmt.Println("  # List available modules")
	fmt.Println("  mib-importer dashboard list --pollable")
	fmt.Println()
}
