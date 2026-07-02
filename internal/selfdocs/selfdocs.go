// Package selfdocs holds the agent's OWN documentation, embedded into the binary so it
// is available regardless of working directory (and in a deployed single binary). It
// lets the agent answer "what am I / how do I work / how am I operated" questions from
// the real docs instead of guessing. The embedded set is reference docs (how it works
// now) plus the vision doc (design intent + trade-offs); planning/scratchpad docs are
// deliberately left out so the agent doesn't mistake roadmap for current behavior.
package selfdocs

import (
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Kind marks a doc's authority about current behavior.
type Kind string

const (
	// KindReference documents how the agent works today (operational + architecture).
	KindReference Kind = "reference"
	// KindVision is design intent + trade-off analysis; may describe not-yet-built
	// ideas, so it is not authoritative about current behavior.
	KindVision Kind = "vision"
)

// aliases maps a friendly topic name to a canonical one, so a long filename can be
// reached by a short handle (e.g. "vision").
var aliases = map[string]string{
	"vision": "self-extending-agent-design",
}

// Info is a doc's listing form (no body).
type Info struct {
	Topic string
	Title string
	Kind  Kind
	Bytes int
}

type doc struct {
	topic string
	title string
	kind  Kind
	body  string
}

// Docs is the embedded documentation set, queried by the read_self_docs tool.
type Docs struct {
	order   []string // canonical topics, reference first then vision, each alpha
	byTopic map[string]doc
}

// New reads every *.md file from fsys into an in-memory set. The topic is the file's
// base name without extension, lowercased (README.md -> "readme"). A nil or empty fsys
// yields an empty set (the tool then reports it has no docs).
func New(fsys fs.FS) (*Docs, error) {
	d := &Docs{byTopic: map[string]doc{}}
	if fsys == nil {
		return d, nil
	}
	err := fs.WalkDir(fsys, ".", func(p string, de fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if de.IsDir() || !strings.HasSuffix(strings.ToLower(de.Name()), ".md") {
			return nil
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		topic := strings.ToLower(strings.TrimSuffix(de.Name(), path.Ext(de.Name())))
		body := string(b)
		d.byTopic[topic] = doc{topic: topic, title: firstHeading(body), kind: classify(topic), body: body}
		d.order = append(d.order, topic)
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Reference docs first (the current-behavior source), then vision; alpha within.
	sort.Slice(d.order, func(i, j int) bool {
		a, b := d.byTopic[d.order[i]], d.byTopic[d.order[j]]
		if a.kind != b.kind {
			return a.kind == KindReference
		}
		return a.topic < b.topic
	})
	return d, nil
}

// Len reports how many docs are loaded.
func (d *Docs) Len() int { return len(d.order) }

// List returns every doc's listing info, reference first.
func (d *Docs) List() []Info {
	out := make([]Info, 0, len(d.order))
	for _, t := range d.order {
		out = append(out, d.byTopic[t].info())
	}
	return out
}

// Get returns a doc's full body by topic (case-insensitive; accepts a "docs/" prefix,
// a ".md" suffix, or a friendly alias). Errors list the available topics.
func (d *Docs) Get(topic string) (string, error) {
	doc, ok := d.byTopic[d.resolve(topic)]
	if !ok {
		return "", fmt.Errorf("no doc %q; available: %s", topic, strings.Join(d.order, ", "))
	}
	return doc.body, nil
}

// Search ranks docs by token overlap of the query against title+body, most relevant
// first. k <= 0 returns all matches; no overlap yields no results.
func (d *Docs) Search(query string, k int) []Info {
	q := tokenize(query)
	if len(q) == 0 {
		return nil
	}
	type scored struct {
		info  Info
		score int
	}
	var hits []scored
	for _, t := range d.order {
		doc := d.byTopic[t]
		if s := overlap(q, tokenize(doc.title+" "+doc.body)); s > 0 {
			hits = append(hits, scored{doc.info(), s})
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	out := make([]Info, len(hits))
	for i, h := range hits {
		out[i] = h.info
	}
	return out
}

func (dc doc) info() Info {
	return Info{Topic: dc.topic, Title: dc.title, Kind: dc.kind, Bytes: len(dc.body)}
}

// resolve normalizes a requested topic to a canonical key.
func (d *Docs) resolve(topic string) string {
	t := strings.ToLower(strings.TrimSpace(topic))
	t = strings.TrimPrefix(t, "docs/")
	t = strings.TrimSuffix(t, ".md")
	if canonical, ok := aliases[t]; ok {
		return canonical
	}
	return t
}

// classify tags a doc by its topic name. The embedded set is reference docs plus the
// vision doc; anything matching the vision filename is KindVision, the rest reference.
func classify(topic string) Kind {
	if strings.HasPrefix(topic, "self-extending") {
		return KindVision
	}
	return KindReference
}

// firstHeading returns the first "# " heading of a markdown body, for a listing title.
func firstHeading(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if s, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// tokenize splits s into a set of lowercased word tokens.
func tokenize(s string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
	}) {
		set[f] = struct{}{}
	}
	return set
}

// overlap counts tokens of q also present in text.
func overlap(q, text map[string]struct{}) int {
	n := 0
	for t := range q {
		if _, ok := text[t]; ok {
			n++
		}
	}
	return n
}
