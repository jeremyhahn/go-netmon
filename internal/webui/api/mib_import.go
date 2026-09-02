package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/mib/parser"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
)

const maxUploadSize = 10 << 20 // 10 MB

// MIBImportRequestError reports an import request the server refused before
// reading the upload.
type MIBImportRequestError struct {
	Field string
	Value string
}

func (e *MIBImportRequestError) Error() string {
	return "mib import: invalid " + e.Field + " " + strconv.Quote(e.Value) +
		": want true or false"
}

// MIBUploadError reports an uploaded MIB file that could not be written to, or
// read back from, the server's MIB directory.
type MIBUploadError struct {
	Op   string
	Path string
	Err  error
}

func (e *MIBUploadError) Error() string {
	return "mib import: " + e.Op + " " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

func (e *MIBUploadError) Unwrap() error { return e.Err }

// MIBRefreshError reports a module whose objects could not be reloaded into the
// poller's metadata store.
type MIBRefreshError struct {
	Module string
	Err    error
}

func (e *MIBRefreshError) Error() string {
	return "mib import: reload objects for module " + strconv.Quote(e.Module) +
		": " + e.Err.Error()
}

func (e *MIBRefreshError) Unwrap() error { return e.Err }

// MIBParseError reports a MIB source file the parser could not turn into a
// module.
//
// Stage separates the two failures that look alike and mean different things:
// "file" is an upload that is not a MIB this parser can read, which is the
// caller's input, while "module" is the parser failing to finish a file it had
// already accepted, which is the server's problem.
type MIBParseError struct {
	Stage string
	Name  string
	Err   error
}

func (e *MIBParseError) Error() string {
	if e.Stage == "file" {
		return "failed to parse MIB file: " + e.Err.Error()
	}
	return "failed to parse module: " + e.Err.Error()
}

func (e *MIBParseError) Unwrap() error { return e.Err }

// MIBLoaderError reports that this server has no usable MIB parser, so it
// cannot accept a module however well formed the upload is.
type MIBLoaderError struct {
	Reason string
}

func (e *MIBLoaderError) Error() string { return e.Reason }

// mibUpload is an uploaded MIB file that has been written into the server's MIB
// directory, together with whatever content it displaced.
//
// A MIB is stored under the name it was uploaded with, so re-uploading a module
// overwrites the copy the stored record points at. Keeping the displaced bytes
// means a file that turns out not to parse can be undone, instead of leaving
// the record pointing at something unparseable until someone notices.
type mibUpload struct {
	// root is the uploaded-MIB directory, held open. Every operation below
	// names a file relative to it and is resolved by the kernel against that
	// open directory, so a name that would leave it cannot be followed.
	root *os.Root
	// name is the file's name within root, never a path.
	name     string
	path     string
	previous []byte
	existed  bool
}

// close releases the directory handle. It is separate from restore because a
// successful upload also has to release it.
func (u *mibUpload) close() {
	if u.root != nil {
		// A directory handle; closing it cannot affect what was written
		// through it.
		_ = u.root.Close()
	}
}

// restore puts back whatever the upload displaced, removing the uploaded file
// when it displaced nothing.
func (u *mibUpload) restore() error {
	if !u.existed {
		if err := u.root.Remove(u.name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return &MIBUploadError{Op: "remove", Path: u.path, Err: err}
		}
		return nil
	}
	if err := u.root.WriteFile(u.name, u.previous, 0o644); err != nil {
		return &MIBUploadError{Op: "restore", Path: u.path, Err: err}
	}
	return nil
}

// importedMIB is what the server did with an uploaded module.
type importedMIB struct {
	record   *storage.MIB
	replaced bool

	// unresolved counts objects the module defines that came out with no OID
	// and were therefore discarded. It is nearly always zero; when it is not,
	// the module is partly or wholly unusable and the caller has to be told,
	// since a module that stores fewer objects than it declares is otherwise
	// indistinguishable from one that is simply small.
	unresolved int
}

// handleImportMIB imports an uploaded MIB file.
//
// With ?replace=true it instead refreshes a module that is already imported:
// the record keeps its ID, name and tags, every device and profile assignment
// stays attached, and only the parsed metadata — objects, object count and
// description — is rewritten. Without it, uploading a module that is already
// stored is a 409 rather than an overwrite.
func (s *Server) handleImportMIB(w http.ResponseWriter, r *http.Request) {
	// Limit upload size
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

	// Parse multipart form
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		http.Error(w, "file too large or invalid form", http.StatusBadRequest)
		return
	}

	replace, err := replaceRequested(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tags := parseTagList(r.FormValue("tags"))

	// Get the uploaded file
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "failed to read file: "+err.Error(), http.StatusBadRequest)
		return
	}
	// The uploaded part is read-only; closing it cannot affect the copy below.
	defer func() { _ = file.Close() }()

	name := filepath.Base(header.Filename)
	if name == "." || name == string(filepath.Separator) {
		http.Error(w, "upload has no usable file name", http.StatusBadRequest)
		return
	}

	outcome, err := s.importMIBSource(r.Context(), name, file, tags, replace)
	if err != nil {
		log.Printf("Failed to import MIB %s: %v", name, err)
		writeImportError(w, err)
		return
	}

	verb := "imported"
	status := http.StatusCreated
	if outcome.replaced {
		verb = "updated"
		status = http.StatusOK
	}

	log.Printf("Successfully %s MIB: %s (%d objects)", verb, outcome.record.Name, outcome.record.ObjectCount)

	respondJSON(w, status, map[string]interface{}{
		"mib":        outcome.record,
		"replaced":   outcome.replaced,
		"unresolved": outcome.unresolved,
		"message": fmt.Sprintf("Successfully %s MIB %s with %d objects",
			verb, outcome.record.Name, outcome.record.ObjectCount),
	})
}

// replaceRequested reports whether the caller asked to refresh a module that is
// already imported. It is opt-in, so an upload can never silently overwrite a
// module someone else imported.
func replaceRequested(r *http.Request) (bool, error) {
	raw := r.URL.Query().Get("replace")
	if raw == "" {
		raw = r.FormValue("replace")
	}
	if raw == "" {
		return false, nil
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, &MIBImportRequestError{Field: "replace", Value: raw}
	}
	return value, nil
}

// parseTagList splits the comma-separated tags field of an upload.
func parseTagList(field string) []string {
	if field == "" {
		return nil
	}
	var tags []string
	for _, tag := range strings.Split(field, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// moduleParser returns the loader's parser, which has every configured MIB path
// on its search list so vendor modules can resolve their imports.
func (s *Server) moduleParser() (*parser.Parser, error) {
	if s.mibLoader == nil {
		return nil, &MIBLoaderError{Reason: "MIB loader not available"}
	}

	parserInterface := s.mibLoader.GetParser()
	if parserInterface == nil {
		return nil, &MIBLoaderError{Reason: "MIB parser not available"}
	}

	loaderParser, ok := parserInterface.(*parser.Parser)
	if !ok {
		return nil, &MIBLoaderError{Reason: "MIB parser type assertion failed"}
	}
	return loaderParser, nil
}

// importMIBSource stores one MIB source file and records the module it defines.
//
// It is the whole of what importing a MIB means, and it is deliberately the only
// implementation of it: the upload endpoint and the configuration importer both
// go through here, so a bundle restored onto a fresh instance produces exactly
// the records a hand upload would, including the loader refresh that makes the
// module's objects available to the poller without a restart.
//
// The parser is resolved before anything is written, because a server that
// cannot parse has no business replacing a MIB file that currently parses. A
// file that turns out not to parse is rolled back, so a rejected import leaves
// neither a stored record nor a stray file behind.
func (s *Server) importMIBSource(
	ctx context.Context,
	fileName string,
	content io.Reader,
	tags []string,
	replace bool,
) (*importedMIB, error) {
	loaderParser, err := s.moduleParser()
	if err != nil {
		return nil, err
	}

	upload, err := s.saveUploadedMIB(fileName, content)
	if err != nil {
		return nil, err
	}
	// The directory handle is only needed while the upload can still be undone;
	// every path below is past that point once this returns.
	defer upload.close()

	moduleInfo, err := loaderParser.LoadModuleFromFile(upload.path)
	if err != nil {
		undoUpload(upload, "parse")
		return nil, &MIBParseError{Stage: "file", Name: fileName, Err: err}
	}

	oids, report, err := loaderParser.ParseModuleWithReport(moduleInfo.Name)
	if err != nil {
		undoUpload(upload, "parse")
		return nil, &MIBParseError{Stage: "module", Name: moduleInfo.Name, Err: err}
	}
	if unresolved := report.UnresolvedCount(); unresolved > 0 {
		log.Printf("MIB %s: %d of %d objects resolved to no OID and were discarded",
			moduleInfo.Name, unresolved, unresolved+len(oids))
	}

	stored, err := s.storeParsedMIB(ctx, moduleInfo.Name, storage.MIBContent{
		FilePath:    upload.path,
		Description: moduleInfo.Description,
		ObjectCount: len(oids),
	}, tags, replace)
	if err != nil {
		return nil, err
	}
	stored.unresolved = report.UnresolvedCount()
	return stored, nil
}

// saveUploadedMIB writes an upload into the server's MIB directory, keeping the
// bytes it displaced so the write can be undone.
func (s *Server) saveUploadedMIB(name string, content io.Reader) (*mibUpload, error) {
	mibDir := filepath.Join(s.mibStoragePath, "uploaded")

	// The MIB store is opened once and everything below is named relative to
	// it, so no path built from an upload is ever handed to the os package.
	//
	// The caller has already reduced name to its base, which stops "../" — but
	// a base name is still followed through a symbolic link, so a link planted
	// in the upload directory would have had os.Create write through it to
	// anywhere this process can reach. A name resolved against an open
	// directory is refused by the kernel instead.
	//
	// #nosec G703 -- mibStoragePath is this server's own configured storage
	// directory, not anything a request supplies. It is the one path here that
	// names a location to the os package, which is what makes every path after
	// it relative to an open handle.
	store, err := os.OpenRoot(s.mibStoragePath)
	if err != nil {
		return nil, &MIBUploadError{Op: "open storage", Path: s.mibStoragePath, Err: err}
	}
	// The store handle exists only to reach the uploaded/ directory below.
	defer func() { _ = store.Close() }()

	if mkdirErr := store.MkdirAll("uploaded", 0o755); mkdirErr != nil {
		return nil, &MIBUploadError{Op: "create directory", Path: mibDir, Err: mkdirErr}
	}

	root, err := store.OpenRoot("uploaded")
	if err != nil {
		return nil, &MIBUploadError{Op: "open directory", Path: mibDir, Err: err}
	}

	path := filepath.Join(mibDir, name)
	upload := &mibUpload{root: root, name: name, path: path}

	previous, err := root.ReadFile(name)
	switch {
	case err == nil:
		upload.previous, upload.existed = previous, true
	case !errors.Is(err, os.ErrNotExist):
		upload.close()
		return nil, &MIBUploadError{Op: "read existing", Path: path, Err: err}
	}

	dst, err := root.Create(name)
	if err != nil {
		upload.close()
		return nil, &MIBUploadError{Op: "create", Path: path, Err: err}
	}

	if _, err := io.Copy(dst, content); err != nil {
		// The handle is about to be abandoned; the copy error is the one that
		// matters and restore rewrites the file anyway.
		_ = dst.Close()
		undoUpload(upload, "write")
		return nil, &MIBUploadError{Op: "write", Path: path, Err: err}
	}
	// Closed explicitly so a flush failure is caught before the file is parsed.
	if err := dst.Close(); err != nil {
		undoUpload(upload, "write")
		return nil, &MIBUploadError{Op: "write", Path: path, Err: err}
	}

	return upload, nil
}

// undoUpload rolls back an upload, logging a rollback that itself fails. There
// is nothing left to return the failure to — the caller is already reporting
// why the upload was rejected — but it must not pass silently, because it means
// a stored module now points at a file that does not parse.
func undoUpload(upload *mibUpload, stage string) {
	if err := upload.restore(); err != nil {
		log.Printf("WARNING: %s failed and the previous MIB file could not be restored: %v",
			stage, err)
	}
}

// storeParsedMIB records a freshly parsed module, either as a new import or as
// an in-place refresh of the record that already holds its name.
func (s *Server) storeParsedMIB(
	ctx context.Context,
	name string,
	content storage.MIBContent,
	tags []string,
	replace bool,
) (*importedMIB, error) {
	existing, err := s.storage.GetMIBByName(ctx, name)

	var notFound *storage.MIBNotFoundError
	switch {
	case errors.As(err, &notFound):
		// Not imported yet. Refreshing a module the server has never seen is an
		// import: the caller asked for the stored state to match this file, and
		// a refresh run across a whole MIB tree must not fail on the one module
		// that was never imported or has since been deleted.
		return s.createParsedMIB(ctx, name, content, tags)
	case err != nil:
		return nil, err
	case !replace:
		return nil, &storage.MIBExistsError{Name: existing.Name, ID: existing.ID}
	}

	// Reload the objects first. They are what the poller reads; the record is a
	// summary of them. In this order a failed reload leaves the record
	// describing the last refresh that did land, and RefreshedAt only advances
	// when the objects really moved.
	if refreshErr := s.refreshModuleObjects(name); refreshErr != nil {
		return nil, refreshErr
	}

	record, err := s.storage.ReplaceMIBContent(ctx, name, content)
	if err != nil {
		return nil, err
	}
	return &importedMIB{record: record, replaced: true}, nil
}

// createParsedMIB stores a module that has not been imported before.
func (s *Server) createParsedMIB(
	ctx context.Context,
	name string,
	content storage.MIBContent,
	tags []string,
) (*importedMIB, error) {
	record := &storage.MIB{
		Name:        name,
		FilePath:    content.FilePath,
		Description: content.Description,
		ObjectCount: content.ObjectCount,
		Tags:        tags,
	}
	if err := s.storage.CreateMIB(ctx, record); err != nil {
		return nil, err
	}

	// A module that is stored but not yet in the loader's object store is still
	// usable: the loader loads it at startup and on demand. Report the failure
	// rather than failing an import that has otherwise succeeded.
	if err := s.refreshModuleObjects(name); err != nil {
		log.Printf("Warning: %v", err)
	} else {
		log.Printf("Successfully loaded MIB module %s into loader storage", name)
	}

	return &importedMIB{record: record}, nil
}

// refreshModuleObjects reloads a module's objects in the loader's metadata
// store. The loader drops the module's existing objects first, so an object the
// new parse no longer defines does not linger and get selected for polling.
func (s *Server) refreshModuleObjects(name string) error {
	if s.mibLoader == nil {
		return &MIBStorageError{Reason: "MIB loader not available"}
	}
	if err := s.mibLoader.LoadModule(name); err != nil {
		return &MIBRefreshError{Module: name, Err: err}
	}

	// What the catalog can describe has just changed, and the held copy is now
	// one module out of date. Re-read it rather than waiting out the refresh
	// interval, so the dashboard drawn straight after an import describes the
	// module that was just imported.
	//
	// Detached from the request: an operator's browser closing the connection
	// after the upload succeeded must not abandon the re-read, or the catalog
	// stays stale until the timer comes round.
	go s.RefreshCatalog(context.WithoutCancel(context.Background()))

	return nil
}

// writeImportError reports why an upload could not be stored.
//
// A module that is already imported is a conflict, not a server fault, and the
// body names the module and the way to refresh it: the workaround an operator
// otherwise reaches for — delete, then import — drops every device and profile
// assignment the module had.
func writeImportError(w http.ResponseWriter, err error) {
	var exists *storage.MIBExistsError
	if errors.As(err, &exists) {
		http.Error(w, conflictMessage(exists), http.StatusConflict)
		return
	}

	var loaderErr *MIBLoaderError
	if errors.As(err, &loaderErr) {
		http.Error(w, loaderErr.Reason, http.StatusInternalServerError)
		return
	}

	var parseErr *MIBParseError
	if errors.As(err, &parseErr) {
		if parseErr.Stage == "file" {
			http.Error(w, parseErr.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, parseErr.Error(), http.StatusInternalServerError)
		return
	}

	var uploadErr *MIBUploadError
	if errors.As(err, &uploadErr) {
		http.Error(w, "failed to save file", http.StatusInternalServerError)
		return
	}

	var refresh *MIBRefreshError
	if errors.As(err, &refresh) {
		http.Error(w, refresh.Error()+"; the stored module was left unchanged",
			http.StatusInternalServerError)
		return
	}

	http.Error(w, "failed to store MIB: "+err.Error(), http.StatusInternalServerError)
}

// conflictMessage explains what to do about a module that is already imported.
func conflictMessage(e *storage.MIBExistsError) string {
	return fmt.Sprintf(
		"MIB %s is already imported as id %d. "+
			"To refresh its objects in place, keeping its id, tags and every device and "+
			"profile assignment, re-import it with POST /api/mibs/import?replace=true "+
			"(CLI: netmon mib import --update). "+
			"DELETE /api/mibs/%d first only if you mean to drop those assignments.",
		e.Name, e.ID, e.ID)
}

// handleUpdateMIBTags updates the tags for a MIB
func (s *Server) handleUpdateMIBTags(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid MIB ID", http.StatusBadRequest)
		return
	}

	var req struct {
		Tags []string `json:"tags"`
	}
	if decodeErr := json.NewDecoder(r.Body).Decode(&req); decodeErr != nil {
		http.Error(w, "invalid request body: "+decodeErr.Error(), http.StatusBadRequest)
		return
	}

	if updateErr := s.storage.UpdateMIBTags(r.Context(), id, req.Tags); updateErr != nil {
		http.Error(w, "failed to update MIB tags: "+updateErr.Error(), http.StatusInternalServerError)
		return
	}

	// Get updated MIB to return
	mib, err := s.storage.GetMIB(r.Context(), id)
	if err != nil {
		http.Error(w, "failed to get updated MIB", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"mib":     mib,
		"message": "Tags updated successfully",
	})
}
