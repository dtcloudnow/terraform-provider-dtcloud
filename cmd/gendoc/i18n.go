package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Translation of page content.
//
// tr.go translates the chrome tfplugindocs generates: section headings and fixed
// sentences. Everything else on a page -- the Description strings from the
// provider schema, the hand-written template text and the comments inside the
// examples -- is looked up in a catalog: the YAML files under i18n/<lang>, each a
// list of `en`/`tr` pairs, one file per page by convention.
//
// A lookup is by the English text with its whitespace collapsed, so rewrapping a
// paragraph keeps its translation but changing a word does not. Missing text
// falls back to English, so a partial catalog still builds; `--check` lists what
// is missing, as YAML ready to be filled in, and what is no longer used. CI runs
// it, which is what keeps the Turkish site complete.

type catalogEntry struct {
	EN string `yaml:"en"`
	TR string `yaml:"tr"`
}

type catalog struct {
	dir     string
	entries map[string]string   // normalised English -> translation
	files   map[string]string   // normalised English -> file that defines it
	used    map[string]bool     // entries some page looked up
	missing map[string][]string // page -> texts with no translation, first-seen order
	seen    map[string]bool     // page + text, so a text is listed once per page
}

func loadCatalog(dir string) (*catalog, error) {
	c := &catalog{
		dir:     dir,
		entries: map[string]string{},
		files:   map[string]string{},
		used:    map[string]bool{},
		missing: map[string][]string{},
		seen:    map[string]bool{},
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return c, nil // nothing translated yet: every page falls back to English
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".yaml" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var entries []catalogEntry
		if err := yaml.Unmarshal(raw, &entries); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for _, e := range entries {
			en, tr := normalise(e.EN), normalise(e.TR)
			if en == "" || tr == "" {
				continue // listed but not translated yet
			}
			if prev, ok := c.entries[en]; ok && prev != tr {
				return fmt.Errorf("%s: %q is already translated differently in %s", path, en, c.files[en])
			}
			c.entries[en] = tr
			c.files[en] = path
		}
		return nil
	})
	return c, err
}

func normalise(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// translatable reports whether a text has words in it. Inline code, punctuation
// and placeholders such as "..." are the same in every language.
func translatable(s string) bool {
	for _, r := range codeSpanR.ReplaceAllString(s, "") {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// text translates one unit of prose. When the catalog has no translation it
// returns s unchanged, records it as missing for page, and reports false.
func (c *catalog) text(page, s string) (string, bool) {
	key := normalise(s)
	if !translatable(key) {
		return s, true
	}
	if v, ok := c.entries[key]; ok {
		c.used[key] = true
		return v, true
	}
	// A sentence generated onto a hand-written one -- the note schema_docs.go
	// appends to every ForceNew argument -- is translated once, on its own.
	for i := strings.LastIndex(key, ". "); i > 0; i = strings.LastIndex(key[:i], ". ") {
		tail, ok := c.entries[key[i+2:]]
		if !ok {
			continue
		}
		c.used[key[i+2:]] = true
		head, ok := c.text(page, key[:i+1])
		if !ok {
			return s, false
		}
		return head + " " + tail, true
	}
	if !c.seen[page+"\x00"+key] {
		c.seen[page+"\x00"+key] = true
		c.missing[page] = append(c.missing[page], key)
	}
	return s, false
}

// report writes what `--check` found and returns how many problems there were:
// texts with no translation, and translations nothing looked up any more.
func (c *catalog) report(w io.Writer) int {
	problems := 0

	pages := make([]string, 0, len(c.missing))
	for page := range c.missing {
		pages = append(pages, page)
	}
	sort.Strings(pages)
	for _, page := range pages {
		texts := c.missing[page]
		problems += len(texts)
		fmt.Fprintf(w, "\n# missing from %s\n", filepath.ToSlash(filepath.Join(c.dir, page+".yaml")))
		for _, t := range texts {
			fmt.Fprintf(w, "- en: |-\n    %s\n  tr: \"\"\n", t)
		}
	}

	var unused []string
	for en := range c.entries {
		if !c.used[en] {
			unused = append(unused, en)
		}
	}
	sort.Strings(unused)
	for _, en := range unused {
		problems++
		fmt.Fprintf(w, "\n# no longer used, remove it from %s:\n#   %s\n", filepath.ToSlash(c.files[en]), en)
	}
	return problems
}

// ---- walking a page ----------------------------------------------------------

var (
	headingRe  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	listItemRe = regexp.MustCompile(`^(\s*)([-*]|\d+\.)\s+(.*)$`)
	calloutRe  = regexp.MustCompile(`^(->|~>|!>)\s+(.*)$`)
	tableSepRe = regexp.MustCompile(`^[\s|:-]+$`)

	// A tfplugindocs schema entry: "- `name` (Type) Description (see [below ...](#...))".
	schemaItemRe = regexp.MustCompile("^(\\s*[-*] `[^`]+` \\([^)]*\\))(?: (.*?))??( \\(see \\[below for nested schema\\]\\(#[^)]+\\)\\))?$")

	// A whole-line comment in an example, and a comment after code on a line.
	commentRe  = regexp.MustCompile(`^(\s*)#(?: (.*))?$`)
	trailingRe = regexp.MustCompile(`^(.*\S)(\s+# )(.+)$`)
)

// translatedLanguages are the fence languages whose `#` comments are prose.
var translatedLanguages = map[string]bool{"": true, "terraform": true, "hcl": true, "shell": true, "sh": true, "bash": true, "yaml": true}

// translate renders the prose of one page through the catalog. Code is left as
// it is apart from its comments, and so is anything that is markup, not text.
func (g *generator) translate(page, body string) string {
	if g.cat == nil {
		return body
	}
	lines := strings.Split(body, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		trimmed := strings.TrimSpace(lines[i])
		_, chrome := trLine[trimmed] // tr.go's to translate, such as "Read-Only:"
		switch {
		case trimmed == "" || chrome || strings.HasPrefix(trimmed, "<"):
			out = append(out, lines[i])
			i++
		case strings.HasPrefix(trimmed, "```"):
			end := i + 1
			for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "```") {
				end++
			}
			out = append(out, lines[i])
			out = append(out, g.translateCode(page, strings.TrimPrefix(trimmed, "```"), lines[i+1:end])...)
			if end < len(lines) {
				out = append(out, lines[end])
			}
			i = end + 1
		case strings.HasPrefix(trimmed, "#"):
			out = append(out, g.translateHeading(page, lines[i]))
			i++
		case strings.HasPrefix(trimmed, "|"):
			out = append(out, g.translateRow(page, lines[i]))
			i++
		case listItemRe.MatchString(lines[i]):
			end := i + 1
			for end < len(lines) && continuesItem(lines[end]) {
				end++
			}
			out = append(out, g.translateItem(page, lines[i:end])...)
			i = end
		default:
			end := i + 1
			for end < len(lines) && continuesParagraph(lines[end]) {
				end++
			}
			out = append(out, g.translateParagraph(page, lines[i:end])...)
			i = end
		}
	}
	return strings.Join(out, "\n")
}

func startsBlock(trimmed string) bool {
	return trimmed == "" || strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "|") ||
		strings.HasPrefix(trimmed, "<") || strings.HasPrefix(trimmed, "#")
}

func continuesItem(line string) bool {
	return strings.HasPrefix(line, " ") && !startsBlock(strings.TrimSpace(line)) && !listItemRe.MatchString(line)
}

func continuesParagraph(line string) bool {
	return !startsBlock(strings.TrimSpace(line)) && !listItemRe.MatchString(line)
}

func indentOf(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

func joinTrimmed(lines []string) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = strings.TrimSpace(l)
	}
	return strings.Join(parts, " ")
}

func (g *generator) translateParagraph(page string, lines []string) []string {
	text, marker := joinTrimmed(lines), ""
	if m := calloutRe.FindStringSubmatch(text); m != nil {
		text, marker = m[2], m[1]+" "
	}
	v, ok := g.cat.text(page, text)
	if !ok || v == text {
		return lines
	}
	return []string{indentOf(lines[0]) + marker + v}
}

func (g *generator) translateItem(page string, lines []string) []string {
	m := listItemRe.FindStringSubmatch(lines[0])
	text := joinTrimmed(append([]string{m[3]}, lines[1:]...))

	if s := schemaItemRe.FindStringSubmatch(m[1] + m[2] + " " + text); s != nil {
		if s[2] == "" {
			return lines
		}
		v, ok := g.cat.text(page, s[2])
		if !ok {
			return lines
		}
		return []string{s[1] + " " + v + s[3]}
	}

	v, ok := g.cat.text(page, text)
	if !ok || v == text {
		return lines
	}
	return []string{m[1] + m[2] + " " + v}
}

func (g *generator) translateHeading(page, line string) string {
	trimmed := strings.TrimSpace(line)
	m := headingRe.FindStringSubmatch(trimmed)
	if m == nil {
		return line
	}
	// Generated headings are tr.go's; a type name is the same in every language.
	if _, ok := trLine[trimmed]; ok || nestedSchemaRe.MatchString(trimmed) ||
		strings.HasSuffix(m[2], " (Resource)") || strings.HasSuffix(m[2], " (Data Source)") {
		return line
	}
	v, ok := g.cat.text(page, m[2])
	if !ok || v == m[2] {
		return line
	}
	return indentOf(line) + m[1] + " " + v
}

func (g *generator) translateRow(page, line string) string {
	if tableSepRe.MatchString(line) {
		return line
	}
	cells := strings.Split(strings.TrimSpace(line), "|")
	for i := 1; i < len(cells)-1; i++ {
		cell := strings.TrimSpace(cells[i])
		if v, ok := g.cat.text(page, cell); ok && v != cell {
			cells[i] = " " + v + " "
		}
	}
	return indentOf(line) + strings.Join(cells, "|")
}

// translateCode translates the comments in an example and nothing else, so the
// example itself can never stop being valid. Consecutive comment lines are one
// unit of text and are rewrapped after translation.
func (g *generator) translateCode(page, lang string, lines []string) []string {
	if !translatedLanguages[strings.ToLower(strings.TrimSpace(lang))] {
		return lines
	}
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		m := commentRe.FindStringSubmatch(lines[i])
		if m == nil || m[2] == "" {
			out = append(out, g.translateTrailing(page, lines[i]))
			i++
			continue
		}
		texts, end := []string{m[2]}, i+1
		for end < len(lines) {
			next := commentRe.FindStringSubmatch(lines[end])
			if next == nil || next[1] != m[1] || next[2] == "" {
				break
			}
			texts = append(texts, next[2])
			end++
		}
		text := strings.Join(texts, " ")
		// A single token -- `<vm-id>:<port-id>`, say -- is a placeholder, not prose.
		if !strings.Contains(strings.TrimSpace(text), " ") {
			out = append(out, lines[i:end]...)
		} else if v, ok := g.cat.text(page, text); ok && v != text {
			out = append(out, wrapComment(m[1], v)...)
		} else {
			out = append(out, lines[i:end]...)
		}
		i = end
	}
	return out
}

func (g *generator) translateTrailing(page, line string) string {
	m := trailingRe.FindStringSubmatch(line)
	// An odd number of quotes means the `#` is inside a string, not a comment.
	if m == nil || strings.Count(m[1], `"`)%2 != 0 {
		return line
	}
	v, ok := g.cat.text(page, m[3])
	if !ok || v == m[3] {
		return line
	}
	return m[1] + m[2] + v
}

// wrapComment lays a translated comment out as `#` lines of at most 80 columns.
func wrapComment(indent, text string) []string {
	const width = 80
	var out []string
	line := indent + "#"
	for _, word := range strings.Fields(text) {
		if len(line) > len(indent)+1 && len(line)+1+len(word) > width {
			out = append(out, line)
			line = indent + "#"
		}
		line += " " + word
	}
	return append(out, line)
}
