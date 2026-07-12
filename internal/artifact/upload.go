package artifact

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// This file is the write side of the user-upload lifecycle whose retention rule ReapScratch
// already implements: a file the *user* provided (OriginUser) survives a session close, while
// agent-derived scratch is re-derivable and gets reaped. Storing an upload is therefore two
// inseparable steps — write the bytes, record the provenance — so they live in one function
// rather than being open-coded (and forgotten) by each frontend that grows an upload path.

// maxNameLen bounds a stored filename. Long names are truncated in the stem, keeping the
// extension, since that is what tells the agent (and its tools) how to read the file.
const maxNameLen = 96

// SafeName reduces an untrusted, externally-supplied filename — a Telegram attachment's
// name, say — to a plain basename that is safe to join onto the scratch dir. It is
// deliberately strict rather than clever: everything outside [A-Za-z0-9._-] becomes "_", so
// no separator, traversal ("..", "a/../../etc"), control character, or shell metacharacter
// can survive into a path or into a command the agent later builds around it. Leading dots
// are stripped (no dotfiles, and "." / ".." cannot be the whole name); an empty result
// becomes "upload".
func SafeName(name string) string {
	// Take the leaf first, so a nested name yields "report.csv" rather than a mangled
	// "dir_report.csv". A sender's path convention is not ours: normalize "\" to "/" first, or
	// a Windows-style "..\..\win.ini" would not be a path to filepath.Base at all (on Linux)
	// and would survive as one long name. The mapping below is the actual containment
	// guarantee; this step is only about producing a sensible leaf.
	name = filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), `\`, "/"))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimLeft(b.String(), ".")
	if out == "" {
		return "upload"
	}
	if len(out) > maxNameLen {
		ext := filepath.Ext(out)
		if len(ext) > 16 { // not a real extension — a dotted name; truncate it whole
			ext = ""
		}
		stem := strings.TrimSuffix(out, ext)
		keep := max(maxNameLen-len(ext), 1)
		out = stem[:min(keep, len(stem))] + ext
	}
	return out
}

// SaveUserFile stores an uploaded file in the session scratch directory dir and records it in
// dir's manifest as user-provided. The name is sanitized (SafeName) and made unique, so an
// upload can neither escape dir nor silently overwrite an existing artifact. source describes
// where it came from (e.g. "telegram upload"), for the planner's manifest view.
//
// Recording it with OriginUser is what makes the file durable: ReapScratch preserves user
// files when the session is closed, and only a purge takes them.
//
// It returns the manifest entry (whose Path is relative to dir) and the size written.
func SaveUserFile(dir, name, source string, r io.Reader) (Entry, int64, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Entry{}, 0, fmt.Errorf("create scratch dir %q: %w", dir, err)
	}
	f, err := createUnique(dir, SafeName(name))
	if err != nil {
		return Entry{}, 0, err
	}
	n, err := io.Copy(f, r)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// A half-written upload is worse than none: it would look like a usable file to the
		// agent. Drop it and report the failure.
		_ = os.Remove(f.Name())
		return Entry{}, 0, fmt.Errorf("write upload %q: %w", name, err)
	}

	stored := filepath.Base(f.Name())
	e := Entry{
		Path:        stored,
		Origin:      OriginUser,
		Source:      source,
		Description: fmt.Sprintf("uploaded by the user (%d bytes)", n),
	}
	m, err := New(filepath.Join(dir, ManifestName))
	if err != nil {
		return Entry{}, 0, fmt.Errorf("open manifest: %w", err)
	}
	if err := m.Append(e); err != nil {
		return Entry{}, 0, fmt.Errorf("record upload: %w", err)
	}
	return e, n, nil
}

// createUnique creates dir/name for writing, suffixing the stem ("data-1.csv", "data-2.csv",
// …) until it lands on a name no file holds. O_EXCL makes the check-and-create atomic, so two
// concurrent uploads of the same name cannot both win.
func createUnique(dir, name string) (*os.File, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := range 100 {
		candidate := name
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		f, err := os.OpenFile(filepath.Join(dir, candidate), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			return f, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("create upload %q: %w", candidate, err)
		}
	}
	return nil, fmt.Errorf("create upload %q: too many files with that name", name)
}
