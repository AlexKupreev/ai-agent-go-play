package projects

// Resolve turns a user-facing project reference (a uid, or a word/phrase from a title) into
// a concrete registry entry — the name→path step the switch tool needs (projects.md P3/§4).
// It is deliberately conservative: a wrong match loads the wrong context *and* the wrong
// trust surface, so an ambiguous reference is reported (AmbiguousError) rather than guessed.

import (
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound means no project matched the query.
var ErrNotFound = errors.New("no matching project")

// AmbiguousError reports that a title query matched more than one project, carrying the
// candidates so the caller can ask the user to disambiguate (or supply a uid).
type AmbiguousError struct {
	Query      string
	Candidates []Project
}

func (e *AmbiguousError) Error() string {
	names := make([]string, len(e.Candidates))
	for i, p := range e.Candidates {
		names[i] = fmt.Sprintf("%s (uid:%s)", p.Title, p.UID)
	}
	return fmt.Sprintf("%q matches multiple projects: %s", e.Query, strings.Join(names, "; "))
}

// Resolve finds the project a query names among those under root. Matching, in order:
//
//   - an exact UID (case-insensitive) wins outright — it is the stable, unambiguous handle;
//   - else an exact title (case-insensitive, trimmed) — one match wins, several are ambiguous;
//   - else a title substring (case-insensitive) — one match wins, several are ambiguous.
//
// No match yields ErrNotFound; more than one title match yields *AmbiguousError.
func Resolve(root, query string) (Project, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return Project{}, ErrNotFound
	}
	ps, err := List(root)
	if err != nil {
		return Project{}, err
	}

	for _, p := range ps {
		if strings.EqualFold(p.UID, q) {
			return p, nil
		}
	}

	lq := strings.ToLower(q)
	var exact, sub []Project
	for _, p := range ps {
		title := strings.ToLower(strings.TrimSpace(p.Title))
		if title == lq {
			exact = append(exact, p)
		}
		if strings.Contains(title, lq) {
			sub = append(sub, p)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) > 1:
		return Project{}, &AmbiguousError{Query: q, Candidates: exact}
	case len(sub) == 1:
		return sub[0], nil
	case len(sub) > 1:
		return Project{}, &AmbiguousError{Query: q, Candidates: sub}
	}
	return Project{}, ErrNotFound
}
