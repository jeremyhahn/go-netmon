package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/grafana/builder"
	"github.com/jeremyhahn/go-netmon/internal/grafana/template"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

const (
	defaultStoragePath = "/app/data/mib-storage"
	defaultOutputDir   = "./grafana/provisioning/dashboards/_generated"
)

// dashboardCommand handles dashboard generation
// prometheusFlag names the flag that sets the Prometheus the generator queries.
const prometheusFlag = "prometheus-url"

// addPrometheusFlag registers --prometheus-url. It has no default of its own:
// the default lives in the config package, and an unset flag must not mask the
// environment (NETMON_DASHBOARD_PROMETHEUS_URL) or the config file
// (dashboard.prometheus_url).
func addPrometheusFlag(fs *flag.FlagSet) {
	fs.String(prometheusFlag, "", "Prometheus URL to query for metric existence "+
		"(config dashboard.prometheus_url, env NETMON_DASHBOARD_PROMETHEUS_URL)")
}

// builderConfig resolves the builder configuration, taking the Prometheus URL
// from flag > environment > config file > default.
func builderConfig(fs *flag.FlagSet) (*builder.Config, error) {
	overrides := config.FlagOverrides(fs, map[string]string{prometheusFlag: config.KeyDashboardPromURL})
	settings, err := config.Settings(config.Path(), overrides)
	if err != nil {
		return nil, err
	}
	cfg := builder.DefaultConfig()
	cfg.PrometheusURL = settings.GetString(config.KeyDashboardPromURL)
	return cfg, nil
}

func dashboardCommand(args []string) error {
	if len(args) == 0 {
		printDashboardUsage()
		return fmt.Errorf("dashboard subcommand required")
	}

	subcommand := args[0]
	switch subcommand {
	case "generate":
		return dashboardGenerate(args[1:])
	case "generate-all":
		return dashboardGenerateAll(args[1:])
	case "list":
		return dashboardList(args[1:])
	case "clean":
		return dashboardClean(args[1:])
	case "help", "-h", "--help":
		printDashboardUsage()
		return nil
	default:
		printDashboardUsage()
		return fmt.Errorf("unknown dashboard command: %s", subcommand)
	}
}

// dashboardGenerate generates dashboard for specific module
func dashboardGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)

	storagePath := fs.String("storage-path", defaultStoragePath, "Path to MIB storage")
	outputDir := fs.String("output-dir", defaultOutputDir, "Output directory for dashboards")
	overwrite := fs.Bool("overwrite", false, "Overwrite existing dashboard")
	validate := fs.Bool("validate", true, "Validate JSON before writing")
	addPrometheusFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		return fmt.Errorf("module name required\n\nUsage: mib-importer dashboard generate <module-name> [options]")
	}

	moduleName := fs.Arg(0)

	fmt.Printf("Generating dashboard for %s...\n", moduleName)

	// Open storage
	store, err := storage.New(*storagePath)
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	// Dashboard generation only reads; closing has nothing left to flush.
	defer func() { _ = store.Close() }()

	// Create builder
	bcfg, err := builderConfig(fs)
	if err != nil {
		return err
	}
	b := builder.New(bcfg, store)

	// Validate module
	if validateErr := b.ValidateModule(moduleName); validateErr != nil {
		return fmt.Errorf("module validation failed: %w", validateErr)
	}

	// Generate dashboard
	start := time.Now()
	spec, err := b.Generate(moduleName)
	if err != nil {
		return fmt.Errorf("failed to generate dashboard: %w", err)
	}

	// Validate if requested
	if *validate {
		if err := spec.Validate(); err != nil {
			return fmt.Errorf("dashboard validation failed: %w", err)
		}
	}

	// Write to file
	writer := template.NewWriter(*outputDir)
	filename := fmt.Sprintf("%s.json", moduleName)

	// Check if file exists and overwrite flag
	fullPath := filepath.Join(*outputDir, filename)
	if !*overwrite {
		if _, err := os.Stat(fullPath); err == nil {
			return fmt.Errorf("dashboard already exists: %s (use --overwrite to replace)", fullPath)
		}
	}

	if err := writer.WriteSpec(spec, filename); err != nil {
		return fmt.Errorf("failed to write dashboard: %w", err)
	}

	duration := time.Since(start)
	panelCount := len(spec.Panels)

	fmt.Printf("Generated dashboard for %s (%d panels) in %v\n", moduleName, panelCount, duration.Round(time.Millisecond))
	fmt.Printf("Output: %s\n", fullPath)

	return nil
}

// dashboardGenerateAll generates dashboards for all loaded modules
func dashboardGenerateAll(args []string) error {
	fs := flag.NewFlagSet("generate-all", flag.ExitOnError)

	storagePath := fs.String("storage-path", defaultStoragePath, "Path to MIB storage")
	outputDir := fs.String("output-dir", defaultOutputDir, "Output directory for dashboards")
	overwrite := fs.Bool("overwrite", false, "Overwrite existing dashboards")
	validate := fs.Bool("validate", true, "Validate JSON before writing")
	generateIndex := fs.Bool("index", true, "Generate index.json catalog")
	addPrometheusFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}

	fmt.Println("Generating dashboards for all modules...")
	fmt.Println()

	// Open storage
	store, err := storage.New(*storagePath)
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	// Dashboard generation only reads; closing has nothing left to flush.
	defer func() { _ = store.Close() }()

	// Create builder
	bcfg, err := builderConfig(fs)
	if err != nil {
		return err
	}
	b := builder.New(bcfg, store)

	// Generate all dashboards
	start := time.Now()
	specs, err := b.GenerateAll()
	if err != nil {
		return fmt.Errorf("failed to generate dashboards: %w", err)
	}

	if len(specs) == 0 {
		return fmt.Errorf("no dashboards generated (no modules with pollable OIDs)")
	}

	// Validate if requested
	if *validate {
		for _, spec := range specs {
			if err := spec.Validate(); err != nil {
				return fmt.Errorf("dashboard validation failed for %s: %w", spec.Module.Name, err)
			}
		}
	}

	// Write dashboards
	writer := template.NewWriter(*outputDir)

	// Clean output directory if overwrite is enabled
	if *overwrite {
		if err := writer.Clean(*outputDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to clean output directory: %v\n", err)
		}
	}

	// Track statistics
	generated := 0
	skipped := 0
	failed := 0

	for i, spec := range specs {
		moduleName := spec.Module.Name
		filename := fmt.Sprintf("%s.json", moduleName)
		fullPath := filepath.Join(*outputDir, filename)

		// Check if exists
		if !*overwrite {
			if _, err := os.Stat(fullPath); err == nil {
				fmt.Printf("[%d/%d] %s... skipped (exists)\n", i+1, len(specs), moduleName)
				skipped++
				continue
			}
		}

		// Write dashboard
		if err := writer.WriteSpec(spec, filename); err != nil {
			fmt.Printf("[%d/%d] %s... failed (%v)\n", i+1, len(specs), moduleName, err)
			failed++
			continue
		}

		panelCount := len(spec.Panels)
		fmt.Printf("[%d/%d] %s... OK (%d panels)\n", i+1, len(specs), moduleName, panelCount)
		generated++
	}

	// Generate index if requested
	if *generateIndex && generated > 0 {
		if err := writer.GenerateIndexFromSpecs(specs); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to generate index: %v\n", err)
		} else {
			fmt.Println()
			fmt.Printf("Generated index.json with %d dashboards\n", len(specs))
		}
	}

	duration := time.Since(start)

	// Print summary
	fmt.Println()
	fmt.Println("=== Summary ===")
	fmt.Printf("Generated:  %d\n", generated)
	if skipped > 0 {
		fmt.Printf("Skipped:    %d\n", skipped)
	}
	if failed > 0 {
		fmt.Printf("Failed:     %d\n", failed)
	}
	fmt.Printf("Duration:   %v\n", duration.Round(time.Millisecond))
	fmt.Printf("Output:     %s\n", *outputDir)

	return nil
}

// dashboardList lists available modules
func dashboardList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)

	storagePath := fs.String("storage-path", defaultStoragePath, "Path to MIB storage")
	showPollable := fs.Bool("pollable", false, "Show pollable OID count")
	verbose := fs.Bool("verbose", false, "Show detailed information")
	addPrometheusFlag(fs)

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Open storage
	store, err := storage.New(*storagePath)
	if err != nil {
		return fmt.Errorf("failed to open storage: %w", err)
	}
	// Dashboard generation only reads; closing has nothing left to flush.
	defer func() { _ = store.Close() }()

	// Create builder
	bcfg, err := builderConfig(fs)
	if err != nil {
		return err
	}
	b := builder.New(bcfg, store)

	// List modules
	modules, err := b.ListModules()
	if err != nil {
		return fmt.Errorf("failed to list modules: %w", err)
	}

	if len(modules) == 0 {
		fmt.Println("No modules found in storage")
		return nil
	}

	fmt.Printf("Available modules (%d):\n\n", len(modules))

	for _, module := range modules {
		if *verbose {
			fmt.Printf("%-30s %s\n", module.Name, module.Description)
			fmt.Printf("  OIDs: %d\n", module.OIDCount)
			fmt.Printf("  Loaded: %s\n", module.LoadedAt.Format("2006-01-02 15:04:05"))

			if *showPollable {
				count, err := b.GetModulePollableCount(module.Name)
				if err == nil {
					fmt.Printf("  Pollable: %d\n", count)
				}
			}
			fmt.Println()
		} else {
			if *showPollable {
				count, err := b.GetModulePollableCount(module.Name)
				pollableStr := "?"
				if err == nil {
					pollableStr = fmt.Sprintf("%d", count)
					// Add indicator if no pollable OIDs
					if count == 0 {
						pollableStr += " (no pollable OIDs)"
					}
				}
				fmt.Printf("  %-30s (pollable: %s)\n", module.Name, pollableStr)
			} else {
				fmt.Printf("  %s\n", module.Name)
			}
		}
	}

	return nil
}

// dashboardClean removes generated dashboards
func dashboardClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)

	outputDir := fs.String("output-dir", defaultOutputDir, "Output directory to clean")
	force := fs.Bool("force", false, "Skip confirmation prompt")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Check if directory exists
	if _, err := os.Stat(*outputDir); os.IsNotExist(err) {
		fmt.Printf("Output directory does not exist: %s\n", *outputDir)
		return nil
	}

	// List files to be removed
	writer := template.NewWriter(*outputDir)
	dashboards, err := writer.ListDashboards()
	if err != nil {
		return fmt.Errorf("failed to list dashboards: %w", err)
	}

	if len(dashboards) == 0 {
		fmt.Println("No dashboards to clean")
		return nil
	}

	// Show files to be removed
	fmt.Printf("The following dashboards will be removed from %s:\n\n", *outputDir)
	for _, dashboard := range dashboards {
		fmt.Printf("  %s\n", dashboard)
	}
	fmt.Printf("\nTotal: %d dashboards\n\n", len(dashboards))

	// Confirm unless force is set
	if !*force {
		fmt.Print("Continue? (y/n): ")
		var confirm string
		if _, err := fmt.Scanln(&confirm); err != nil {
			return fmt.Errorf("failed to read confirmation: %w", err)
		}

		if confirm != "y" && confirm != "Y" {
			fmt.Println("Canceled")
			return nil
		}
	}

	// Clean
	if err := writer.Clean(*outputDir); err != nil {
		return fmt.Errorf("failed to clean dashboards: %w", err)
	}

	fmt.Printf("Removed %d dashboards\n", len(dashboards))

	return nil
}

// printDashboardUsage prints dashboard command usage
func printDashboardUsage() {
	fmt.Println("Dashboard Generator - Generate Grafana dashboards from MIBs")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  mib-importer dashboard <command> [options]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  generate <module>     Generate dashboard for specific MIB module")
	fmt.Println("  generate-all          Generate dashboards for all loaded modules")
	fmt.Println("  list                  List available MIB modules")
	fmt.Println("  clean                 Remove all generated dashboards")
	fmt.Println("  help                  Show this help message")
	fmt.Println()
	fmt.Println("Generate Options:")
	fmt.Println("  --storage-path <path>   Path to MIB storage (default: /app/data/mib-storage)")
	fmt.Println("  --output-dir <dir>      Output directory (default: ./grafana/provisioning/dashboards/_generated)")
	fmt.Println("  --overwrite             Overwrite existing dashboards")
	fmt.Println("  --validate              Validate JSON before writing (default: true)")
	fmt.Println("  --index                 Generate index.json catalog (generate-all only)")
	fmt.Println()
	fmt.Println("List Options:")
	fmt.Println("  --storage-path <path>   Path to MIB storage")
	fmt.Println("  --pollable              Show pollable OID count")
	fmt.Println("  --verbose               Show detailed information")
	fmt.Println()
	fmt.Println("Clean Options:")
	fmt.Println("  --output-dir <dir>      Output directory to clean")
	fmt.Println("  --force                 Skip confirmation prompt")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  # Generate dashboard for IF-MIB")
	fmt.Println("  mib-importer dashboard generate IF-MIB")
	fmt.Println()
	fmt.Println("  # Generate all dashboards")
	fmt.Println("  mib-importer dashboard generate-all --output-dir ./grafana/dashboards")
	fmt.Println()
	fmt.Println("  # List available modules with pollable OID counts")
	fmt.Println("  mib-importer dashboard list --pollable")
	fmt.Println()
	fmt.Println("  # Clean generated dashboards")
	fmt.Println("  mib-importer dashboard clean")
	fmt.Println()
}
