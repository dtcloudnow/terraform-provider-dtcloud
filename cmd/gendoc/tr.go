package main

import (
	"regexp"
	"strings"
)

// Turkish rendering of the generated reference.
//
// The rule is the same one the dtctl generator uses: a string is looked up
// verbatim and, if it is missing, the English source is emitted unchanged. A
// partial catalogue therefore still produces a valid (mixed) page and the site
// build stays green, so entries can be filled in over time without ever
// blocking a docs update.
//
// What is translated here is the *chrome*: the section headings and the fixed
// sentences tfplugindocs generates. The body text of a page comes from the
// Description strings on the provider schema and is left in English until those
// carry a Turkish translation of their own.

// tr translates a label used by this generator (sidebar labels, folder names).
func tr(g *generator, en string) string {
	if g.lang != "tr" {
		return en
	}
	if v, ok := trUI[en]; ok {
		return v
	}
	return en
}

var trUI = map[string]string{
	"Terraform Provider": "Terraform Sağlayıcısı",
	"Guides":             "Kılavuzlar",
	"Resources":          "Kaynaklar",
	"Data Sources":       "Veri Kaynakları",

	// Registry subcategories, used as headings on the section intro pages.
	"Compute":    "Bilişim",
	"Networking": "Ağ",
	"Storage":    "Depolama",
	"Images":     "İmajlar",
	"Access":     "Erişim",
	"Account":    "Hesap",
	"Other":      "Diğer",
}

// trLine holds the whole-line strings tfplugindocs generates.
var trLine = map[string]string{
	"## Example Usage": "## Örnek Kullanım",
	"## Schema":        "## Şema",
	"## Import":        "## İçe Aktarma",
	"### Required":     "### Zorunlu",
	"### Optional":     "### İsteğe Bağlı",
	"### Read-Only":    "### Salt Okunur",
	"Required:":        "Zorunlu:",
	"Optional:":        "İsteğe Bağlı:",
	"Read-Only:":       "Salt Okunur:",
	"Import is supported using the following syntax:": "İçe aktarma aşağıdaki söz dizimiyle desteklenir:",
	"## Authentication": "## Kimlik Doğrulama",
}

// trPhrase holds fragments that appear inside a line.
var trPhrase = [][2]string{
	{"(see [below for nested schema](", "(bkz. [aşağıdaki iç içe şema]("},
	{" (Resource)", " (Kaynak)"},
	{" (Data Source)", " (Veri Kaynağı)"},
}

var nestedSchemaRe = regexp.MustCompile("^### Nested Schema for `(.+)`$")

// trBody translates the generated chrome inside a page body. Fenced code blocks
// are never touched: a translated example would no longer be valid HCL.
func trBody(g *generator, body string) string {
	if g.lang != "tr" {
		return body
	}
	lines := strings.Split(body, "\n")
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if v, ok := trLine[strings.TrimSpace(line)]; ok {
			lines[i] = v
			continue
		}
		if m := nestedSchemaRe.FindStringSubmatch(line); m != nil {
			lines[i] = "### `" + m[1] + "` için iç içe şema"
			continue
		}
		for _, p := range trPhrase {
			lines[i] = strings.ReplaceAll(lines[i], p[0], p[1])
		}
	}
	return strings.Join(lines, "\n")
}
