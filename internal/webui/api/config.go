package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

// The kinds a configuration error can have. A client switches on these rather
// than on the message, so the wording can improve without breaking anything.
const (
	ConfigErrorBundle      = "bundle"       // the upload is not a readable configuration
	ConfigErrorSchema      = "schema"       // the document's schema_version is not one this build reads
	ConfigErrorValidation  = "validation"   // the document's own fields are wrong
	ConfigErrorMissingMIBs = "missing_mibs" // modules the configuration needs are neither carried nor imported
	ConfigErrorMIBImport   = "mib_import"   // a carried MIB source could not be imported
	ConfigErrorSafety      = "safety_export"
	ConfigErrorStorage     = "storage"
)

// ConfigErrorResponse is the body every failed configuration request carries.
//
// It is JSON rather than the plain text the older endpoints use because the
// answers here are lists — every field that is wrong, every module that is
// missing — and a client that has to show an operator what to fix cannot do it
// from a sentence.
type ConfigErrorResponse struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`

	// Problems is set when the document's fields were rejected.
	Problems []storage.ConfigProblem `json:"problems,omitempty"`

	// MissingModules is set when the configuration references MIB modules this
	// instance does not have and the file does not carry.
	MissingModules []string `json:"missing_modules,omitempty"`

	// SchemaVersion and SupportedSchemaVersion are set when the document was
	// written by a build this one cannot read.
	SchemaVersion          int `json:"schema_version,omitempty"`
	SupportedSchemaVersion int `json:"supported_schema_version,omitempty"`
}

// ConfigRequestError reports a query parameter the configuration endpoints will
// not act on.
type ConfigRequestError struct {
	Parameter string
	Value     string
	Reason    string
}

func (e *ConfigRequestError) Error() string {
	return "config: " + e.Parameter + "=" + strconv.Quote(e.Value) + ": " + e.Reason
}

// handleExportConfig writes this instance's whole logical configuration.
//
// By default it answers with a bundle: a gzipped tar holding config.json and the
// MIB source files the configuration references, so the archive restores onto an
// empty instance on its own. With ?format=json it answers with config.json
// alone, which is the artifact to read, diff and attach to a change request.
func (s *Server) handleExportConfig(w http.ResponseWriter, r *http.Request) {
	opts, asJSON, err := exportRequest(r)
	if err != nil {
		writeConfigError(w, http.StatusBadRequest, ConfigErrorBundle, err)
		return
	}

	result, err := newConfigService(s).export(r.Context(), opts)
	if err != nil {
		writeConfigError(w, http.StatusInternalServerError, ConfigErrorStorage, err)
		return
	}

	// The whole artifact is built before anything is written, so a failure to
	// produce it is a 500 rather than a truncated download that looks complete.
	body, contentType, fileName, err := renderExport(result, asJSON)
	if err != nil {
		writeConfigError(w, http.StatusInternalServerError, ConfigErrorStorage, err)
		return
	}

	if len(result.MissingSources) > 0 {
		w.Header().Set("X-Netmon-Missing-MIB-Sources", strings.Join(result.MissingSources, ","))
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Content-Disposition", `attachment; filename="`+fileName+`"`)
	// The body is a download, not a page: it is served under an explicit
	// content type as an attachment, and nosniff stops a browser deciding
	// otherwise about a MIB description that happens to contain markup.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(body); err != nil { // #nosec G705 -- an attachment served under an explicit content type with nosniff, never rendered as a page
		s.log().WarnContext(r.Context(), "writing the configuration export", "error", err)
	}
}

// renderExport serializes an export in the requested form.
func renderExport(result *ExportResult, asJSON bool) (body []byte, contentType, fileName string, err error) {
	stamp := result.Bundle.Config.ExportedAt.UTC().Format("20060102T150405Z")

	if asJSON {
		document, marshalErr := marshalConfig(result.Bundle.Config)
		if marshalErr != nil {
			return nil, "", "", marshalErr
		}
		return document, "application/json", "netmon-config-" + stamp + ".json", nil
	}

	var archive bytes.Buffer
	if writeErr := writeConfigBundle(&archive, result.Bundle); writeErr != nil {
		return nil, "", "", writeErr
	}
	return archive.Bytes(), "application/gzip", "netmon-config-" + stamp + ".tar.gz", nil
}

// exportRequest reads the export's query parameters.
func exportRequest(r *http.Request) (opts storage.ConfigExportOptions, asJSON bool, err error) {
	switch format := r.URL.Query().Get("format"); format {
	case "", "bundle":
	case "json":
		asJSON = true
	default:
		return opts, false, &ConfigRequestError{
			Parameter: "format", Value: format, Reason: "want bundle or json",
		}
	}

	switch mibs := r.URL.Query().Get("mibs"); mibs {
	case "", "all":
	case "referenced":
		opts.ReferencedMIBsOnly = true
	default:
		return opts, false, &ConfigRequestError{
			Parameter: "mibs", Value: mibs, Reason: "want all or referenced",
		}
	}

	return opts, asJSON, nil
}

// handleImportConfig imports a configuration, replacing what is here with it.
//
// It is a dry run unless ?apply=true. That is not a convenience: the import
// deletes every device, profile and assignment the document does not contain, so
// the default has to be the one that cannot destroy anything, and applying has
// to be something the caller said out loud. It follows the same shape as
// POST /api/mibs/import?replace=true and scripts/restore.sh --yes.
func (s *Server) handleImportConfig(w http.ResponseWriter, r *http.Request) {
	// A bundle carrying a few hundred MIB modules is parsed module by module,
	// which takes far longer than an ordinary request. Extending this one
	// handler's deadline keeps the rest of the server's budget tight.
	extendDeadlines(w, ConfigImportBudget)

	apply, err := applyRequested(r)
	if err != nil {
		writeConfigError(w, http.StatusBadRequest, ConfigErrorBundle, err)
		return
	}

	upload, err := readConfigUpload(w, r)
	if err != nil {
		writeConfigError(w, http.StatusBadRequest, ConfigErrorBundle, err)
		return
	}
	bundle, err := parseConfigUpload(upload)
	if err != nil {
		writeConfigError(w, http.StatusBadRequest, ConfigErrorBundle, err)
		return
	}

	// One call either way. A dry run and an apply validate the same document
	// against the same instance, so planning first and then applying would only
	// do the work twice and give the two paths a chance to disagree.
	service := newConfigService(s)
	run := service.plan
	if apply {
		run = service.apply
	}

	report, err := run(r.Context(), bundle)
	if err != nil {
		status, kind := configErrorStatus(err)
		writeConfigError(w, status, kind, err)
		return
	}

	respondJSON(w, http.StatusOK, report)
}

// extendDeadlines gives one handler more time than the server's defaults.
//
// A response controller that cannot set a deadline means the connection does not
// support one, which is the case in tests and behind some proxies; there is
// nothing to report and nothing to do about it.
func extendDeadlines(w http.ResponseWriter, budget time.Duration) {
	controller := http.NewResponseController(w) //nolint:bodyclose // not a response body
	deadline := time.Now().Add(budget)
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
}

// applyRequested reports whether the caller asked for the import to be applied.
func applyRequested(r *http.Request) (bool, error) {
	raw := r.URL.Query().Get("apply")
	if raw == "" {
		return false, nil
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, &ConfigRequestError{
			Parameter: "apply", Value: raw, Reason: "want true or false",
		}
	}
	return value, nil
}

// readConfigUpload reads the configuration a client sent, from a multipart form
// field named "file" or from the request body directly.
//
// Both are accepted because both are natural: a browser file input produces a
// multipart form, and every other client produces a body. The bytes are what
// decide whether it is a bundle or a bare document, so neither route has to
// declare which it is sending.
func readConfigUpload(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBundleUploadSize)

	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(maxBundleUploadSize); err != nil {
			return nil, &BundleFormatError{Reason: "the upload could not be read: " + err.Error()}
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			return nil, &BundleFormatError{
				Reason: `the form has no "file" field: ` + err.Error(),
			}
		}
		// The uploaded part is read-only; closing it cannot affect the bytes
		// already read out of it.
		defer func() { _ = file.Close() }()
		return readAllLimited(file)
	}

	return readAllLimited(r.Body)
}

// readAllLimited reads an upload, refusing one larger than the bundle limit.
func readAllLimited(source io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(source, maxBundleUploadSize+1))
	if err != nil {
		return nil, &BundleFormatError{Reason: "the upload could not be read: " + err.Error()}
	}
	if len(data) > maxBundleUploadSize {
		return nil, &BundleFormatError{
			Reason: "is larger than " + strconv.Itoa(maxBundleUploadSize/(1<<20)) + " MB",
		}
	}
	return data, nil
}

// configErrorStatus maps a failed import to the status and kind that describe
// it.
//
// The distinction that matters most is between a document that is wrong, which
// is a 400 and the operator's to fix, and a document that is right for an
// instance holding modules this one does not, which is a 409 and a prerequisite
// to satisfy. Answering both with 400 would send someone editing a file that has
// nothing wrong with it.
func configErrorStatus(err error) (status int, kind string) {
	var missing *storage.ConfigMIBMissingError
	if errors.As(err, &missing) {
		return http.StatusConflict, ConfigErrorMissingMIBs
	}

	var schemaErr *storage.ConfigSchemaError
	if errors.As(err, &schemaErr) {
		return http.StatusBadRequest, ConfigErrorSchema
	}

	var invalid *storage.ConfigValidationError
	if errors.As(err, &invalid) {
		return http.StatusBadRequest, ConfigErrorValidation
	}

	var mibImport *ConfigMIBImportError
	if errors.As(err, &mibImport) {
		var loaderErr *MIBLoaderError
		if errors.As(err, &loaderErr) {
			return http.StatusInternalServerError, ConfigErrorStorage
		}
		return http.StatusBadRequest, ConfigErrorMIBImport
	}

	var safety *ConfigSafetyExportError
	if errors.As(err, &safety) {
		return http.StatusInternalServerError, ConfigErrorSafety
	}

	var format *BundleFormatError
	var entry *BundleEntryError
	var sum *BundleChecksumError
	if errors.As(err, &format) || errors.As(err, &entry) || errors.As(err, &sum) {
		return http.StatusBadRequest, ConfigErrorBundle
	}

	return http.StatusInternalServerError, ConfigErrorStorage
}

// writeConfigError answers with the structured body a client can act on.
func writeConfigError(w http.ResponseWriter, status int, kind string, err error) {
	body := ConfigErrorResponse{Kind: kind, Message: err.Error()}

	var invalid *storage.ConfigValidationError
	if errors.As(err, &invalid) {
		body.Problems = invalid.Problems
	}

	var missing *storage.ConfigMIBMissingError
	if errors.As(err, &missing) {
		body.MissingModules = missing.Modules
	}

	var schemaErr *storage.ConfigSchemaError
	if errors.As(err, &schemaErr) {
		body.SchemaVersion = schemaErr.Found
		body.SupportedSchemaVersion = schemaErr.Supported
	}

	respondJSON(w, status, body)
}
