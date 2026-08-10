// Package importer downloads MIB source files and prepares them for parsing.
package importer

import (
	"fmt"
	"time"
)

// Statistics holds import operation statistics
type Statistics struct {
	TotalMIBs     int
	Downloaded    int
	AlreadyExists int
	NotFound      int
	Failed        int
	Duration      time.Duration
	FailedMIBs    []string
	NotFoundMIBs  []string
}

// String returns a formatted statistics summary
func (s *Statistics) String() string {
	return fmt.Sprintf(
		"Total: %d | Downloaded: %d | Already Exists: %d | Not Found: %d | Failed: %d | Duration: %s",
		s.TotalMIBs, s.Downloaded, s.AlreadyExists, s.NotFound, s.Failed, s.Duration,
	)
}

// SuccessRate returns the percentage of successfully downloaded MIBs
func (s *Statistics) SuccessRate() float64 {
	if s.TotalMIBs == 0 {
		return 0
	}
	return float64(s.Downloaded+s.AlreadyExists) / float64(s.TotalMIBs) * 100
}

// DownloadOptions configures the MIB download behavior
type DownloadOptions struct {
	OutputDir       string
	BaseURL         string
	Overwrite       bool
	MaxConcurrency  int
	Timeout         time.Duration
	ProgressUpdates bool
}

// DefaultDownloadOptions returns sensible defaults
func DefaultDownloadOptions() *DownloadOptions {
	return &DownloadOptions{
		OutputDir:       "./mibs",
		BaseURL:         "https://raw.githubusercontent.com/cisco/cisco-mibs/main/v2",
		Overwrite:       false,
		MaxConcurrency:  10,
		Timeout:         30 * time.Second,
		ProgressUpdates: true,
	}
}
