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
	"strconv"
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

// Hit is a ranked section. Ref() is what the model passes back as topic+section.
type Hit struct {
	Topic   string
	Kind    Kind
	Slug    string
	Heading string
	Bytes   int
}

// Ref renders a hit as "topic#slug".
func (h Hit) Ref() string { return h.Topic + "#" + h.Slug }

type doc struct {
	topic    string
	title    string
	kind     Kind
	body     string
	sections []Section
}

// Section is one "## " chunk of a doc — the unit the agent retrieves and cites. The
// text before the first "## " is the "intro" section.
type Section struct {
	Slug    string // stable handle, e.g. "trust-tiers"
	Heading string // the "## " text; "" for the intro
	Bytes   int
	body    string
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
		d.byTopic[topic] = doc{
			topic:    topic,
			title:    firstHeading(body),
			kind:     classify(topic),
			body:     body,
			sections: splitSections(body),
		}
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
	dc, ok := d.byTopic[d.resolve(topic)]
	if !ok {
		return "", d.unknown(topic)
	}
	return dc.body, nil
}

// Outline returns a doc's section list (headings + sizes), so the model can pick one
// instead of pulling the whole document.
func (d *Docs) Outline(topic string) (Info, []Section, error) {
	dc, ok := d.byTopic[d.resolve(topic)]
	if !ok {
		return Info{}, nil, d.unknown(topic)
	}
	return dc.info(), dc.sections, nil
}

// Section returns one section's body. sel matches a slug exactly, then by prefix, then
// by substring, then as a 1-based index. Errors list the doc's sections.
func (d *Docs) Section(topic, sel string) (string, error) {
	dc, ok := d.byTopic[d.resolve(topic)]
	if !ok {
		return "", d.unknown(topic)
	}
	if sec, ok := dc.find(sel); ok {
		return sec.body, nil
	}
	slugs := make([]string, len(dc.sections))
	for i, sec := range dc.sections {
		slugs[i] = sec.Slug
	}
	return "", fmt.Errorf("no section %q in %q; sections: %s", sel, dc.topic, strings.Join(slugs, ", "))
}

// Search ranks sections by token overlap of the query against heading+body, most
// relevant first (heading matches count double). k <= 0 returns all matches.
func (d *Docs) Search(query string, k int) []Hit {
	q := tokenize(query)
	if len(q) == 0 {
		return nil
	}
	type scored struct {
		hit   Hit
		score int
	}
	var hits []scored
	for _, t := range d.order {
		dc := d.byTopic[t]
		for _, sec := range dc.sections {
			s := overlap(q, tokenize(sec.body)) + 2*overlap(q, tokenize(dc.title+" "+sec.Heading))
			if s > 0 {
				hits = append(hits, scored{Hit{
					Topic:   dc.topic,
					Kind:    dc.kind,
					Slug:    sec.Slug,
					Heading: sec.Heading,
					Bytes:   sec.Bytes,
				}, s})
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	out := make([]Hit, len(hits))
	for i, h := range hits {
		out[i] = h.hit
	}
	return out
}

// find locates a section by slug (exact, then prefix, then substring) or 1-based index.
func (dc doc) find(sel string) (Section, bool) {
	s := slugify(strings.TrimPrefix(strings.TrimSpace(sel), "#"))
	if s == "" {
		return Section{}, false
	}
	for _, match := range []func(string) bool{
		func(slug string) bool { return slug == s },
		func(slug string) bool { return strings.HasPrefix(slug, s) },
		func(slug string) bool { return strings.Contains(slug, s) },
	} {
		for _, sec := range dc.sections {
			if match(sec.Slug) {
				return sec, true
			}
		}
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(dc.sections) {
		return dc.sections[n-1], true
	}
	return Section{}, false
}

func (d *Docs) unknown(topic string) error {
	return fmt.Errorf("no doc %q; available: %s", topic, strings.Join(d.order, ", "))
}

// splitSections cuts a markdown body at every top-level "## " heading, ignoring those
// inside fenced code blocks. The text before the first heading is the "intro".
func splitSections(body string) []Section {
	var (
		out     []Section
		heading string
		cur     []string
		fenced  bool
	)
	flush := func() {
		text := strings.TrimSpace(strings.Join(cur, "\n"))
		if text == "" && heading == "" {
			return
		}
		full := text
		if heading != "" {
			full = "## " + heading + "\n\n" + text
		}
		out = append(out, Section{Slug: sectionSlug(heading, len(out)), Heading: heading, Bytes: len(full), body: full})
		cur = nil
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if h, ok := strings.CutPrefix(line, "## "); ok && !fenced {
			flush()
			heading = strings.TrimSpace(h)
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return out
}

func sectionSlug(heading string, i int) string {
	if heading == "" {
		return "intro"
	}
	if s := slugify(heading); s != "" {
		return s
	}
	return fmt.Sprintf("section-%d", i+1)
}

// slugify lowercases and joins the alphanumeric runs of s with hyphens.
func slugify(s string) string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
	})
	return strings.Join(fields, "-")
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
