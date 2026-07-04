package projects

// Create implements the write side of the registry (projects.md P2): minting a new
// project directory <root>/<slug>-<uid>/ with a seeded .agent/project.md marker, and —
// for promotion — moving scratch artifacts into it. This is the only path that writes to
// the registry; List (create.go's read counterpart) never mutates. Switching *into* the
// created project (re-anchoring the workspace) is P3 and lives in the cmd layer, not here.

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v3"
)

// maxSlugLen bounds the human-readable slug portion of the folder name; the UID keeps
// identity, so the slug is only a convenience and need not carry the whole title.
const maxSlugLen = 40

// CreateOptions describes a new project. Title is required (it seeds the slug and the
// marker's human name); Description is optional metadata. FromPaths, when set, are scratch
// artifacts moved into the new project directory — this is what turns create into
// "promote" (projects.md §3), keeping promotion a variant of create rather than a fourth verb.
type CreateOptions struct {
	Title       string
	Description string
	FromPaths   []string
}

// Create mints a new project under root and returns its registry entry. It creates
// <root>/<slug>-<uid>/.agent/project.md with title/uid/created/last_active/description,
// creating root itself if this is the first project. A blank title is rejected. Any
// FromPaths are moved into the project directory after the marker is written (promotion);
// a move failure is reported but the project already exists by then.
func Create(root string, opts CreateOptions) (Project, error) {
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		return Project{}, fmt.Errorf("project title is required")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return Project{}, fmt.Errorf("create projects root: %w", err)
	}

	// Mint <slug>-<uid>, retrying on the (vanishingly unlikely) UID collision so the
	// directory we create is guaranteed fresh — the folder IS the registration.
	slug := slugify(title)
	var dir, uid string
	for attempts := 0; ; attempts++ {
		uid = mintUID()
		dir = filepath.Join(root, slug+"-"+uid)
		if err := os.Mkdir(dir, 0700); err == nil {
			break
		} else if !os.IsExist(err) {
			return Project{}, fmt.Errorf("create project dir: %w", err)
		}
		if attempts >= 8 {
			return Project{}, fmt.Errorf("could not mint a unique project id")
		}
	}

	now := time.Now().UTC()
	p := Project{
		UID:         uid,
		Title:       title,
		Description: strings.TrimSpace(opts.Description),
		Created:     now,
		LastActive:  now,
		Path:        dir,
	}
	if err := writeMarker(dir, p); err != nil {
		return Project{}, err
	}
	if err := movePaths(dir, opts.FromPaths); err != nil {
		return p, err // the project exists; surface the partial-promotion error
	}
	return p, nil
}

// writeMarker seeds <dir>/.agent/project.md with the project's frontmatter. The marker is
// marshalled (not string-formatted) so a title or description with YAML-special characters
// round-trips cleanly back through List's parser.
func writeMarker(dir string, p Project) error {
	adir := filepath.Join(dir, ".agent")
	if err := os.MkdirAll(adir, 0700); err != nil {
		return fmt.Errorf("create .agent dir: %w", err)
	}
	front, err := yaml.Marshal(marker{
		Title:       p.Title,
		UID:         p.UID,
		Created:     p.Created.Format(time.RFC3339),
		LastActive:  p.LastActive.Format(time.RFC3339),
		Description: p.Description,
	})
	if err != nil {
		return fmt.Errorf("marshal project marker: %w", err)
	}
	content := "---\n" + string(front) + "---\n"
	if err := os.WriteFile(filepath.Join(adir, "project.md"), []byte(content), 0600); err != nil {
		return fmt.Errorf("write project marker: %w", err)
	}
	return nil
}

// movePaths relocates each scratch artifact into the project directory under its base name
// (promotion). Same-workspace moves are a plain rename; a missing source or a name collision
// inside the project is an error so promotion never silently drops or clobbers work.
func movePaths(dir string, paths []string) error {
	for _, src := range paths {
		src = strings.TrimSpace(src)
		if src == "" {
			continue
		}
		dst := filepath.Join(dir, filepath.Base(src))
		if _, err := os.Stat(dst); err == nil {
			return fmt.Errorf("promote %s: %s already exists in the project", src, filepath.Base(src))
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("promote %s: %w", src, err)
		}
	}
	return nil
}

// slugify derives a filesystem-safe, human-readable slug from a title: lowercase, runs of
// non-alphanumeric characters collapse to a single hyphen, and the result is trimmed and
// length-bounded. An empty result (a title of only punctuation) falls back to "project" so
// the folder name is always well-formed.
func slugify(title string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > maxSlugLen {
		s = strings.Trim(s[:maxSlugLen], "-")
	}
	if s == "" {
		return "project"
	}
	return s
}

// mintUID returns a short, filesystem-safe, collision-resistant identity token: 5 random
// bytes (40 bits) as lowercase unpadded base32 → 8 chars (projects.md §2). The UID is the
// stable handle; the slug can change with a retitle without moving the folder.
func mintUID() string {
	var buf [5]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand should never fail; fall back to a time-derived token rather than panic.
		return strings.ToLower(fmt.Sprintf("t%d", time.Now().UnixNano()%1e10))
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf[:]))
}
