package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

// File uploads let a frontend put a user-provided file into a session's working area, so the
// agent can read it with the tools it already has (shell, run_code) instead of the bytes having
// to reach the model as content. The engine core knows no disk paths, so — exactly as with
// onSessionClose and the scratch reaper — it takes the storage as a seam (FileStore) that the
// cmd layer fills in with the session scratch dir + artifact manifest.
//
// Uploads are optional: with no FileStore wired, the endpoint is not served.

// ErrUploadsDisabled is returned by UploadFile when no FileStore is wired.
var ErrUploadsDisabled = errors.New("file uploads are not enabled")

// maxUploadBytes caps an uploaded file. 20 MB is also the ceiling the Telegram Bot API imposes
// on a bot download, so this bounds every frontend at the same place, and bounds what an
// allowlisted user can write onto the state volume in one request.
const maxUploadBytes = 20 << 20

// FileStore persists a user-provided file into a session's working area and returns where it
// landed. The cmd layer's implementation writes into the session scratch dir and records the
// file in the artifact manifest as user-provided, which is what preserves it across a session
// close (see artifact.SaveUserFile / artifact.ReapScratch).
type FileStore interface {
	SaveUpload(sessionID, name, source string, r io.Reader) (UploadInfo, error)
}

// UploadInfo describes a stored upload. Path is what the agent is told to read — the absolute
// location of the file in its scratch directory.
type UploadInfo struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// SetFileStore wires the store that persists uploaded files, enabling POST /sessions/{id}/files.
// Optional; call it before NewServer, which registers the route only when a store is present.
func (e *Engine) SetFileStore(fs FileStore) { e.files = fs }

// UploadsEnabled reports whether a FileStore is wired.
func (e *Engine) UploadsEnabled() bool { return e.files != nil }

// UploadFile stores r as a file named name in sessionID's working area, attributed to source
// (e.g. "telegram upload"). The session must exist — an upload is scoped to a conversation, and
// its lifecycle (reap on close, delete on purge) is the session's.
func (e *Engine) UploadFile(sessionID, name, source string, r io.Reader) (UploadInfo, error) {
	if !e.SessionsEnabled() {
		return UploadInfo{}, ErrSessionsDisabled
	}
	if e.files == nil {
		return UploadInfo{}, ErrUploadsDisabled
	}
	if _, err := e.sessions.Get(sessionID); err != nil {
		return UploadInfo{}, err
	}
	return e.files.SaveUpload(sessionID, name, source, r)
}

// handleUploadFile serves POST /sessions/{id}/files: a multipart/form-data body with the file in
// the "file" part and an optional "source" field. The response is the stored UploadInfo, whose
// Path the frontend hands to the agent in the turn text.
func handleUploadFile(e *Engine) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validSessionID(id) {
			http.Error(w, "invalid session id", http.StatusBadRequest)
			return
		}
		// Cap the body before reading a byte of it: an upload is the one endpoint where a
		// client streams unbounded data at the state volume. The slack covers the multipart
		// framing around the file itself.
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)

		f, hdr, err := r.FormFile("file")
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, fmt.Sprintf("file exceeds the %d MB upload limit", maxUploadBytes>>20), http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "expected a multipart body with a \"file\" part: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer f.Close()

		info, err := e.UploadFile(id, hdr.Filename, r.FormValue("source"), f)
		if err != nil {
			if errors.Is(err, ErrUploadsDisabled) || errors.Is(err, ErrSessionsDisabled) {
				http.Error(w, err.Error(), http.StatusNotImplemented)
				return
			}
			sessionErrStatus(w, err) // unknown session ⇒ 404
			return
		}
		writeJSON(w, info)
	}
}
