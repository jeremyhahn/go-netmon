package importer

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/net/html"
)

// ParseSupportList extracts MIB names from a Cisco support list HTML file
func ParseSupportList(htmlFile string) ([]string, error) {
	f, err := os.Open(htmlFile) // #nosec G304 -- htmlFile is the support list the operator asked to parse
	if err != nil {
		return nil, fmt.Errorf("failed to open HTML file: %w", err)
	}
	// Read-only handle: closing it cannot affect the parse that already ran.
	defer func() { _ = f.Close() }()

	return ParseSupportListReader(f)
}

// ParseSupportListReader extracts MIB names from an HTML reader
func ParseSupportListReader(r io.Reader) ([]string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	mibSet := make(map[string]struct{})
	extractMIBNames(doc, mibSet)

	// Convert map to sorted slice
	mibs := make([]string, 0, len(mibSet))
	for mib := range mibSet {
		mibs = append(mibs, mib)
	}

	if len(mibs) == 0 {
		return nil, fmt.Errorf("no MIBs found in HTML")
	}

	return mibs, nil
}

// extractMIBNames recursively walks the HTML tree and extracts MIB names from table cells
func extractMIBNames(n *html.Node, mibSet map[string]struct{}) {
	if n.Type == html.ElementNode && (n.Data == "td" || n.Data == "li") {
		// Extract text content from this cell/list item
		text := extractText(n)
		text = strings.TrimSpace(text)

		// Check if this looks like a MIB name
		if isMIBName(text) {
			// Normalize the MIB name
			normalized := normalizeMIBName(text)
			if normalized != "" {
				mibSet[normalized] = struct{}{}
			}
		}
	}

	// Recursively process child nodes
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractMIBNames(c, mibSet)
	}
}

// extractText recursively extracts all text content from a node
func extractText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}

	var text string
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		text += extractText(c)
	}
	return text
}

// isMIBName checks if a string looks like a MIB name
func isMIBName(s string) bool {
	s = strings.TrimSpace(s)

	// Must have some length
	if len(s) < 3 || len(s) > 100 {
		return false
	}

	// Common patterns for MIB names
	patterns := []string{
		"CISCO-",
		"-MIB",
		"-CAPABILITY",
		"SNMPv2-",
		"IF-",
		"IP-",
		"TCP-",
		"UDP-",
		"RFC",
		"IANAifType",
	}

	upper := strings.ToUpper(s)
	for _, pattern := range patterns {
		if strings.Contains(upper, pattern) {
			return true
		}
	}

	return false
}

// normalizeMIBName converts various MIB name formats to a standard format
func normalizeMIBName(s string) string {
	s = strings.TrimSpace(s)

	// Remove common suffixes that might be present
	s = strings.TrimSuffix(s, ".my")
	s = strings.TrimSuffix(s, ".mib")
	s = strings.TrimSuffix(s, ".txt")

	// Remove any parenthetical notes (e.g., "CISCO-SYSTEM-MIB (v1)")
	if idx := strings.Index(s, "("); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}

	// Remove any version suffixes separated by space
	// This must happen before uppercase conversion
	if idx := strings.Index(s, " "); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}

	// Ensure uppercase (standard MIB convention)
	s = strings.ToUpper(s)

	// Validate final format - must not contain spaces (invalid MIB name)
	if len(s) < 3 || strings.Contains(s, " ") {
		return ""
	}

	return s
}

// ParseMultipleSupportLists parses multiple support list files and combines unique MIBs
func ParseMultipleSupportLists(htmlFiles []string) ([]string, error) {
	if len(htmlFiles) == 0 {
		return nil, fmt.Errorf("no HTML files provided")
	}

	mibSet := make(map[string]struct{})

	for _, file := range htmlFiles {
		mibs, err := ParseSupportList(file)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", file, err)
		}

		for _, mib := range mibs {
			mibSet[mib] = struct{}{}
		}
	}

	mibs := make([]string, 0, len(mibSet))
	for mib := range mibSet {
		mibs = append(mibs, mib)
	}

	return mibs, nil
}
