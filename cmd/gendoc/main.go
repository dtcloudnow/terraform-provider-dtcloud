// Command gendoc converts the Terraform Registry documentation in docs/ into
// pages for the DT Cloud Docusaurus site.
//
// It is the second half of a two-step pipeline:
//
//	Go schema + examples  --tfplugindocs-->  docs/  --gendoc-->  docusaurus
//
// Nothing here knows anything about the provider, so adding a resource needs no
// change in this file.
//
// Usage:
//
//	go run ./cmd/gendoc --out ../docusaurus [--lang en|tr]
//	go run ./cmd/gendoc --lang tr --check
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// section is one of the folders the Registry layout defines. The order here is
// the order they appear in the Docusaurus sidebar.
type section struct {
	dir   string // folder name under docs/ and under the site
	label string // sidebar label
}

var sections = []section{
	{"guides", "Guides"},
	{"resources", "Resources"},
	{"data-sources", "Data Sources"},
}

// subcategoryOrder is the order subcategories are listed in. One that appears in
// docs/ but not here sorts last, so a new one shows up rather than disappearing.
var subcategoryOrder = []string{
	"Compute",
	"Networking",
	"Storage",
	"Images",
	"Access",
	"Account",
	"Other",
}

func main() {
	out := flag.String("out", "../docusaurus", "path to the Docusaurus site root")
	in := flag.String("docs", "docs", "path to the generated Registry docs directory")
	lang := flag.String("lang", "en", "language to generate: en|tr")
	slug := flag.String("slug", "terraform", "folder name to write inside the site's docs tree")
	position := flag.Int("position", 11, "sidebar position of the whole section")
	catalogDir := flag.String("catalog", filepath.Join("i18n", "tr"), "translation catalog for --lang tr")
	check := flag.Bool("check", false, "write nothing; list missing and unused translations and fail if there are any (needs --lang tr)")
	flag.Parse()

	var root string
	switch *lang {
	case "en":
		root = filepath.Join(*out, "docs", *slug)
	case "tr":
		root = filepath.Join(*out, "i18n", "tr", "docusaurus-plugin-content-docs", "current", *slug)
	default:
		fmt.Fprintf(os.Stderr, "gendoc: unknown --lang %q (want en or tr)\n", *lang)
		os.Exit(2)
	}

	g := &generator{in: *in, root: root, lang: *lang, slug: *slug, position: *position, check: *check}
	if *lang == "tr" {
		cat, err := loadCatalog(*catalogDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "gendoc: reading the translation catalog: %v\n", err)
			os.Exit(1)
		}
		g.cat = cat
	} else if *check {
		fmt.Fprintln(os.Stderr, "gendoc: --check needs --lang tr: English is the source, there is nothing to check")
		os.Exit(2)
	}

	if err := g.run(); err != nil {
		fmt.Fprintf(os.Stderr, "gendoc: %v\n", err)
		os.Exit(1)
	}

	if *check {
		if n := g.cat.report(os.Stdout); n > 0 {
			fmt.Fprintf(os.Stderr, "\ngendoc: %d translation problem(s) in %s; see above\n", n, *catalogDir)
			os.Exit(1)
		}
		fmt.Printf("gendoc: every %s text is translated\n", *lang)
		return
	}
	fmt.Printf("gendoc: wrote the %s Terraform reference to %s\n", *lang, root)
}

type generator struct {
	in       string
	root     string
	lang     string
	slug     string
	position int
	cat      *catalog // nil for English, the source language
	check    bool     // translate everything, write nothing
}

// page is one converted Registry markdown file.
type page struct {
	id          string // path under docs/ without .md, e.g. "resources/volume"
	name        string // file name without .md, e.g. "volume"
	title       string // page_title from the frontmatter
	subcategory string
	description string
	body        string
}

func (g *generator) run() error {
	if _, err := os.Stat(g.in); err != nil {
		return fmt.Errorf("no docs to convert at %s: run `make docs` first: %w", g.in, err)
	}

	// The whole tree is rewritten on every run so that a resource deleted
	// upstream cannot leave a stale page behind on the site.
	if err := g.reset(g.root); err != nil {
		return err
	}
	if err := g.write(filepath.Join(g.root, "_category_.json"),
		categoryJSON(tr(g, "Terraform Provider"), g.position, true)); err != nil {
		return err
	}

	// The provider page becomes the section's intro.
	index, err := g.readPage(filepath.Join(g.in, "index.md"))
	if err != nil {
		return err
	}
	index.id = "index"
	if err := g.write(filepath.Join(g.root, "intro.md"), g.render(index, introFrontMatter(g))); err != nil {
		return err
	}

	for i, sec := range sections {
		if err := g.writeSection(sec, i+1); err != nil {
			return fmt.Errorf("%s: %w", sec.dir, err)
		}
	}
	return nil
}

func (g *generator) writeSection(sec section, position int) error {
	dir := filepath.Join(g.in, sec.dir)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil // a provider with no guides, for instance
	}
	if err != nil {
		return err
	}

	var pages []page
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p, err := g.readPage(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		p.id = sec.dir + "/" + p.name
		p.description = g.text(p.id, p.description)
		if sec.dir == "guides" {
			p.title = g.text(p.id, p.title) // a guide's title is its sidebar label
		}
		pages = append(pages, p)
	}
	if len(pages) == 0 {
		return nil
	}
	sortPages(pages)

	outDir := filepath.Join(g.root, sec.dir)
	if err := g.write(filepath.Join(outDir, "_category_.json"),
		categoryJSON(tr(g, sec.label), position, true)); err != nil {
		return err
	}
	if err := g.write(filepath.Join(outDir, "intro.md"), g.sectionIntro(sec, pages)); err != nil {
		return err
	}

	for i, p := range pages {
		fm := fmt.Sprintf("---\nsidebar_label: %q\nsidebar_position: %d\n",
			p.sidebarLabel(), i+1)
		if p.description != "" {
			fm += fmt.Sprintf("description: %q\n", firstSentence(p.description))
		}
		fm += "---\n\n"
		if err := g.write(filepath.Join(outDir, p.name+".md"), g.render(p, fm)); err != nil {
			return err
		}
	}
	return nil
}

// sectionIntro is the landing page for a folder: every page in it, grouped by
// the Registry subcategory so the grouping matches registry.terraform.io.
func (g *generator) sectionIntro(sec section, pages []page) string {
	var b strings.Builder
	label := tr(g, sec.label)
	fmt.Fprintf(&b, "---\nsidebar_label: %q\nsidebar_position: 0\n---\n\n", label)
	fmt.Fprintf(&b, "# %s\n\n", label)

	// Guides carry no meaningful subcategory; list them flat.
	if sec.dir == "guides" {
		for _, p := range pages {
			fmt.Fprintf(&b, "- [%s](./%s.md)\n", mdxSafe(p.title), p.name)
		}
		b.WriteString("\n")
		return b.String()
	}

	byCat := map[string][]page{}
	for _, p := range pages {
		byCat[p.categoryLabel()] = append(byCat[p.categoryLabel()], p)
	}
	for _, cat := range orderedCategories(byCat) {
		fmt.Fprintf(&b, "## %s\n\n", tr(g, cat))
		for _, p := range byCat[cat] {
			fmt.Fprintf(&b, "- [`%s`](./%s.md) — %s\n",
				p.sidebarLabel(), p.name, mdxSafe(firstSentence(p.description)))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// render turns a Registry page into a Docusaurus one: the given front matter,
// then the body translated, de-noticed and made safe for MDX.
func (g *generator) render(p page, frontMatter string) string {
	body := strings.TrimSpace(p.body)
	body = g.translate(p.id, body)
	body = trBody(g, body)
	body = dropHTMLComments(body)
	body = hoistAnchors(body)
	body = convertCallouts(body)
	return frontMatter + mdxSafe(body) + "\n"
}

// text translates a single string, such as a page description, for the
// catalog's language. English runs return it unchanged.
func (g *generator) text(page, s string) string {
	if g.cat == nil || strings.TrimSpace(s) == "" {
		return s
	}
	v, _ := g.cat.text(page, s)
	return v
}

var htmlCommentLineRe = regexp.MustCompile(`^\s*<!--.*-->\s*$`)

// dropHTMLComments removes whole-line HTML comments. MDX has no comment syntax
// of that kind, so left in they would print as visible text.
func dropHTMLComments(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence && htmlCommentLineRe.MatchString(line) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// calloutKinds maps the Registry's callout markers to Docusaurus admonitions.
// Docusaurus would otherwise print the marker as literal text.
var calloutKinds = []struct{ marker, kind string }{
	{"-> ", "note"},
	{"~> ", "warning"},
	{"!> ", "danger"},
}

// convertCallouts rewrites each callout paragraph -- from its marker to the
// next blank line -- as a Docusaurus `:::kind` block. Code fences are left
// alone, since `->` is ordinary text inside an example.
func convertCallouts(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out = append(out, line)
			continue
		}
		kind, first := "", ""
		if !inFence {
			for _, c := range calloutKinds {
				if strings.HasPrefix(line, c.marker) {
					kind, first = c.kind, strings.TrimPrefix(line, c.marker)
					break
				}
			}
		}
		if kind == "" {
			out = append(out, line)
			continue
		}
		para := []string{first}
		for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
			i++
			para = append(para, lines[i])
		}
		out = append(out, ":::"+kind, "", strings.Join(para, "\n"), "", ":::")
	}
	return strings.Join(out, "\n")
}

// hoistAnchors moves the id tfplugindocs writes in an empty `<a>` tag onto the
// heading below it. Docusaurus collects anchors from headings only, so without
// this every "see below for nested schema" link is broken and none reaches the
// table of contents.
func hoistAnchors(body string) string {
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		m := anchorOnlyRe.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			out = append(out, lines[i])
			continue
		}
		// The heading may be one blank line below the tag.
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) || !strings.HasPrefix(lines[j], "#") {
			out = append(out, lines[i]) // not the pattern we know; leave it be
			continue
		}
		out = append(out, strings.TrimRight(lines[j], " ")+" {#"+m[1]+"}")
		i = j
	}
	return strings.Join(out, "\n")
}

// ---- reading the Registry pages -------------------------------------------

var (
	fmKeyRe   = regexp.MustCompile(`(?m)^([a-z_]+):\s*(.*)$`)
	quotedRe  = regexp.MustCompile(`^"(.*)"$`)
	anchorRe  = regexp.MustCompile(`<a id="[^"]*"></a>`)
	codeSpanR = regexp.MustCompile("`+[^`]*`+")

	// A line that is nothing but one of tfplugindocs' anchor tags.
	anchorOnlyRe = regexp.MustCompile(`^<a id="([^"]+)"></a>$`)
	// A Docusaurus explicit heading id, which must survive MDX escaping.
	headingIDRe = regexp.MustCompile(`\{#[A-Za-z0-9_-]+\}$`)
)

func (g *generator) readPage(path string) (page, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return page{}, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	p := page{name: strings.TrimSuffix(filepath.Base(path), ".md")}
	front, body := splitFrontMatter(text)
	p.body = body

	// description is a YAML block scalar (`description: |-`), so it is read
	// separately from the single-line keys.
	if i := strings.Index(front, "description: |-"); i >= 0 {
		block := front[i+len("description: |-"):]
		var lines []string
		for _, ln := range strings.Split(block, "\n") {
			if strings.TrimSpace(ln) == "" {
				continue
			}
			if !strings.HasPrefix(ln, "  ") {
				break // a following key at column 0 ends the block
			}
			lines = append(lines, strings.TrimSpace(ln))
		}
		p.description = strings.Join(lines, " ")
		front = front[:i]
	}

	for _, m := range fmKeyRe.FindAllStringSubmatch(front, -1) {
		val := strings.TrimSpace(m[2])
		if q := quotedRe.FindStringSubmatch(val); q != nil {
			val = q[1]
		}
		switch m[1] {
		case "page_title":
			p.title = val
		case "subcategory":
			p.subcategory = val
		}
	}
	return p, nil
}

// splitFrontMatter returns the YAML front matter and the body. A file without
// front matter is all body.
func splitFrontMatter(text string) (string, string) {
	if !strings.HasPrefix(text, "---\n") {
		return "", text
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", text
	}
	return rest[:end], rest[end+len("\n---\n"):]
}

// ---- MDX safety ------------------------------------------------------------

// mdxSafe escapes the characters MDX reads as JSX or as an expression. Code
// blocks, inline code spans and the anchor tags are left exactly as they are.
func mdxSafe(s string) string {
	var out strings.Builder
	inFence := false
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			out.WriteString("\n")
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			out.WriteString(line)
			continue
		}
		if inFence || strings.HasPrefix(line, "    ") {
			out.WriteString(line) // fenced or indented code
			continue
		}
		out.WriteString(escapeLine(line))
	}
	return out.String()
}

func escapeLine(line string) string {
	// Hold back the parts that must survive verbatim, escape what is left, then
	// put them back.
	var kept []string
	protect := func(re *regexp.Regexp, s string) string {
		return re.ReplaceAllStringFunc(s, func(m string) string {
			kept = append(kept, m)
			return fmt.Sprintf("\x00%d\x00", len(kept)-1)
		})
	}
	line = protect(headingIDRe, line)
	line = protect(anchorRe, line)
	line = protect(codeSpanR, line)

	line = strings.ReplaceAll(line, "<", "&lt;")
	line = strings.ReplaceAll(line, "{", "&#123;")
	line = strings.ReplaceAll(line, "}", "&#125;")

	for i, k := range kept {
		line = strings.ReplaceAll(line, fmt.Sprintf("\x00%d\x00", i), k)
	}
	return line
}

// ---- ordering --------------------------------------------------------------

func sortPages(pages []page) {
	rank := func(sub string) int {
		for i, s := range subcategoryOrder {
			if s == sub {
				return i
			}
		}
		return len(subcategoryOrder)
	}
	sort.SliceStable(pages, func(i, j int) bool {
		ri, rj := rank(pages[i].categoryLabel()), rank(pages[j].categoryLabel())
		if ri != rj {
			return ri < rj
		}
		if pages[i].categoryLabel() != pages[j].categoryLabel() {
			return pages[i].categoryLabel() < pages[j].categoryLabel()
		}
		return pages[i].name < pages[j].name
	})
}

func orderedCategories(byCat map[string][]page) []string {
	var known, unknown []string
	seen := map[string]bool{}
	for _, s := range subcategoryOrder {
		if _, ok := byCat[s]; ok {
			known = append(known, s)
			seen[s] = true
		}
	}
	for c := range byCat {
		if !seen[c] {
			unknown = append(unknown, c)
		}
	}
	sort.Strings(unknown)
	return append(known, unknown...)
}

func (p page) categoryLabel() string {
	if strings.TrimSpace(p.subcategory) == "" {
		return "Other"
	}
	return p.subcategory
}

// sidebarLabel is the Terraform type name, which is what a reader scans for.
// The Registry page_title is "dtcloud_volume Resource - dtcloud"; for guides
// there is no type name, so the title stands in.
func (p page) sidebarLabel() string {
	if f := strings.Fields(p.title); len(f) > 0 && strings.HasPrefix(f[0], "dtcloud_") {
		return f[0]
	}
	if p.title != "" {
		return p.title
	}
	return p.name
}

// ---- output helpers --------------------------------------------------------

func introFrontMatter(g *generator) string {
	return fmt.Sprintf("---\nslug: /%s\nsidebar_label: %q\nsidebar_position: 0\n---\n\n",
		g.slug, tr(g, "Terraform Provider"))
}

func categoryJSON(label string, position int, linkIntro bool) string {
	type link struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	v := struct {
		Label    string `json:"label"`
		Position int    `json:"position"`
		Link     *link  `json:"link,omitempty"`
	}{Label: label, Position: position}
	if linkIntro {
		v.Link = &link{Type: "doc", ID: "intro"}
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b) + "\n"
}

// firstSentence trims a description down to something that fits a sidebar
// subtitle or a one-line index entry.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}

// reset empties dir, so a page deleted upstream does not survive on the site.
// A check run writes nothing.
func (g *generator) reset(dir string) error {
	if g.check {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

func (g *generator) write(path, content string) error {
	if g.check {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
