package importer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
)

// Downloader handles MIB file downloads from GitHub
type Downloader struct {
	client  *http.Client
	baseURL string
}

// NewDownloader creates a new MIB downloader
func NewDownloader(baseURL string, timeout time.Duration) *Downloader {
	return &Downloader{
		client: &http.Client{
			Timeout: timeout,
		},
		baseURL: baseURL,
	}
}

// DownloadAll downloads multiple MIBs concurrently with progress tracking
func (d *Downloader) DownloadAll(ctx context.Context, mibs []string, opts *DownloadOptions) (*Statistics, error) {
	if opts == nil {
		opts = DefaultDownloadOptions()
	}

	// Ensure output directory exists
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	start := time.Now()
	stats := &Statistics{
		TotalMIBs:    len(mibs),
		FailedMIBs:   make([]string, 0),
		NotFoundMIBs: make([]string, 0),
	}

	// Use semaphore to limit concurrency
	sem := semaphore.NewWeighted(int64(opts.MaxConcurrency))
	var wg sync.WaitGroup

	// Atomic counters for progress
	var downloaded, alreadyExists, notFound, failed atomic.Int32

	// Channel for progress updates
	var progressMu sync.Mutex
	progressTicker := time.NewTicker(time.Second)
	defer progressTicker.Stop()

	if opts.ProgressUpdates {
		go func() {
			for range progressTicker.C {
				d := downloaded.Load()
				a := alreadyExists.Load()
				n := notFound.Load()
				f := failed.Load()
				total := int32(len(mibs)) // #nosec G115 -- len of an in-memory MIB list, nowhere near 2^31
				completed := d + a + n + f

				if completed >= total {
					return
				}

				fmt.Printf("\rProgress: %d/%d (Downloaded: %d, Exists: %d, Not Found: %d, Failed: %d)",
					completed, total, d, a, n, f)
			}
		}()
	}

	// Download MIBs concurrently
	for _, mib := range mibs {
		if err := sem.Acquire(ctx, 1); err != nil {
			return stats, fmt.Errorf("failed to acquire semaphore: %w", err)
		}

		wg.Add(1)
		go func(mibName string) {
			defer wg.Done()
			defer sem.Release(1)

			outputPath := filepath.Join(opts.OutputDir, mibName+".my")

			// Check if file already exists
			if !opts.Overwrite {
				if _, err := os.Stat(outputPath); err == nil {
					alreadyExists.Add(1)
					return
				}
			}

			// Try to download the MIB
			err := d.downloadMIB(ctx, mibName, outputPath)
			if err != nil {
				if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") {
					notFound.Add(1)
					progressMu.Lock()
					stats.NotFoundMIBs = append(stats.NotFoundMIBs, mibName)
					progressMu.Unlock()
				} else {
					failed.Add(1)
					progressMu.Lock()
					stats.FailedMIBs = append(stats.FailedMIBs, fmt.Sprintf("%s: %v", mibName, err))
					progressMu.Unlock()
				}
				return
			}

			downloaded.Add(1)
		}(mib)
	}

	wg.Wait()

	// Final progress update
	if opts.ProgressUpdates {
		fmt.Println() // New line after progress
	}

	// Update final statistics
	stats.Downloaded = int(downloaded.Load())
	stats.AlreadyExists = int(alreadyExists.Load())
	stats.NotFound = int(notFound.Load())
	stats.Failed = int(failed.Load())
	stats.Duration = time.Since(start)

	return stats, nil
}

// downloadMIB downloads a single MIB file
func (d *Downloader) downloadMIB(ctx context.Context, mibName, outputPath string) error {
	// Try common MIB filename patterns
	filenames := []string{
		mibName + ".my", // Standard format (e.g., CISCO-SYSTEM-MIB.my)
	}

	// If it doesn't already end with -MIB, try adding it
	if !strings.HasSuffix(mibName, "-MIB") {
		filenames = append(filenames, mibName+"-MIB.my")
	}

	// If it ends with -MIB, also try without it
	if strings.HasSuffix(mibName, "-MIB") {
		baseName := strings.TrimSuffix(mibName, "-MIB")
		filenames = append(filenames, baseName+".my")
	}

	var lastErr error
	for _, filename := range filenames {
		url := d.baseURL + "/" + filename

		err := d.downloadFile(ctx, url, outputPath)
		if err == nil {
			return nil
		}

		lastErr = err
		if !strings.Contains(err.Error(), "404") {
			// If it's not a 404, don't try other patterns
			break
		}
	}

	return lastErr
}

// downloadFile downloads a file from a URL to a local path
func (d *Downloader) downloadFile(ctx context.Context, url, outputPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	// Create temporary file first
	tmpPath := outputPath + ".tmp"
	out, err := os.Create(tmpPath) // #nosec G304 -- tmpPath is derived from the caller's configured output directory
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer func() {
		// The success path closes out explicitly below and checks that error;
		// this close only covers the early returns, and the removal of a
		// half-written temp file is best effort by definition.
		_ = out.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()

	// Copy content
	if _, err = io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Close before rename
	if err = out.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}

	// Rename temp file to final name
	if err = os.Rename(tmpPath, outputPath); err != nil {
		return fmt.Errorf("failed to rename file: %w", err)
	}

	return nil
}

// DownloadMIB downloads a single MIB file (convenience method)
func DownloadMIB(ctx context.Context, mibName, baseURL, outputDir string) error {
	opts := DefaultDownloadOptions()
	opts.BaseURL = baseURL
	opts.OutputDir = outputDir
	opts.ProgressUpdates = false

	d := NewDownloader(baseURL, opts.Timeout)
	return d.downloadMIB(ctx, mibName, filepath.Join(outputDir, mibName+".my"))
}
