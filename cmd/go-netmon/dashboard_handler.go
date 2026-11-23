package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/grafana/builder"
	"github.com/jeremyhahn/go-netmon/internal/grafana/template"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
)

// DashboardHandler handles dashboard generation requests
type DashboardHandler struct {
	mibLoader *loader.Loader
	outputDir string
}

// NewDashboardHandler creates a new dashboard handler
func NewDashboardHandler(mibLoader *loader.Loader, outputDir string) *DashboardHandler {
	return &DashboardHandler{
		mibLoader: mibLoader,
		outputDir: outputDir,
	}
}

// HandleGenerate generates dashboards via HTTP endpoint
func (h *DashboardHandler) HandleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Println("Dashboard generation requested via API")
	start := time.Now()

	// Get storage from MIB loader
	// GetStorage hands back a concrete *storage.Storage boxed in an interface, so
	// the interface itself is never nil; the assertion below is the real guard.
	mibStorage, ok := h.mibLoader.GetStorage().(*storage.Storage)
	if !ok || mibStorage == nil {
		http.Error(w, "MIB storage not available", http.StatusInternalServerError)
		return
	}

	// Create builder
	config := &builder.Config{
		PrometheusDataSource: "Prometheus",
		RefreshInterval:      "30s",
		DeviceLabel:          "device",
		OIDLabel:             "oid",
	}
	b := builder.New(config, mibStorage)

	// Generate all dashboards
	specs, err := b.GenerateAll()
	if err != nil {
		log.Printf("Failed to generate dashboards: %v", err)
		http.Error(w, fmt.Sprintf("Failed to generate dashboards: %v", err), http.StatusInternalServerError)
		return
	}

	// Write to files
	writer := template.NewWriter(h.outputDir)
	if err := writer.WriteAllSpecs(specs); err != nil {
		log.Printf("Failed to write dashboards: %v", err)
		http.Error(w, fmt.Sprintf("Failed to write dashboards: %v", err), http.StatusInternalServerError)
		return
	}

	// Generate index
	if err := writer.GenerateIndexFromSpecs(specs); err != nil {
		log.Printf("Warning: Failed to generate index: %v", err)
		// Don't fail the request if index generation fails
	}

	duration := time.Since(start)

	response := map[string]interface{}{
		"success":    true,
		"dashboards": len(specs),
		"output_dir": h.outputDir,
		"duration":   duration.String(),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("dashboard: encoding response: %v", err)
	}

	log.Printf("Generated %d dashboards in %v", len(specs), duration)
}

// HandleList lists available modules
func (h *DashboardHandler) HandleList(w http.ResponseWriter, _ *http.Request) {
	// GetStorage hands back a concrete *storage.Storage boxed in an interface, so
	// the interface itself is never nil; the assertion below is the real guard.
	mibStorage, ok := h.mibLoader.GetStorage().(*storage.Storage)
	if !ok || mibStorage == nil {
		http.Error(w, "MIB storage not available", http.StatusInternalServerError)
		return
	}

	modules, err := mibStorage.ListModules()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list modules: %v", err), http.StatusInternalServerError)
		return
	}

	response := map[string]interface{}{
		"modules": modules,
		"count":   len(modules),
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("dashboard: encoding response: %v", err)
	}
}
