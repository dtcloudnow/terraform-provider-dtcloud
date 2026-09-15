package main

import (
	"strings"
	"testing"
)

func testGenerator(entries map[string]string) *generator {
	c := &catalog{
		entries: map[string]string{},
		files:   map[string]string{},
		used:    map[string]bool{},
		missing: map[string][]string{},
		seen:    map[string]bool{},
	}
	for en, tr := range entries {
		c.entries[normalise(en)] = tr
	}
	return &generator{lang: "tr", cat: c}
}

func TestTranslateBody(t *testing.T) {
	g := testGenerator(map[string]string{
		"A wrapped paragraph of prose.":                      "Satırlara bölünmüş bir paragraf.",
		"Name of the volume.":                                "Diskin adı.",
		"ID of the source.":                                  "Kaynağın kimliği.",
		"Changing this forces a new resource to be created.": "Bu değiştirilirse yeni bir kaynak oluşturulur.",
		"**Bold lead.** Then the rest.":                      "**Kalın giriş.** Sonra gerisi.",
		"A warning that wraps.":                              "Satıra sığmayan bir uyarı.",
		"Build the machine first.":                           "Önce makineyi oluşturun.",
		"Symbol":                                             "Simge",
	})

	body := strings.Join([]string{
		"A wrapped paragraph",
		"of prose.",
		"",
		"- `name` (String) Name of the volume.",
		"- `source` (String) ID of the source. Changing this forces a new resource to be created.",
		"- `timeouts` (Block, Optional) (see [below for nested schema](#nestedblock--timeouts))",
		"",
		"* **Bold lead.** Then",
		"  the rest.",
		"",
		"~> A warning",
		"that wraps.",
		"",
		"| Symbol | `+` |",
		"|--------|-----|",
		"",
		"```terraform",
		"# Build the machine",
		"# first.",
		`resource "dtcloud_vm" "web" {`,
		"  # ...",
		"}",
		"```",
	}, "\n")

	want := strings.Join([]string{
		"Satırlara bölünmüş bir paragraf.",
		"",
		"- `name` (String) Diskin adı.",
		"- `source` (String) Kaynağın kimliği. Bu değiştirilirse yeni bir kaynak oluşturulur.",
		"- `timeouts` (Block, Optional) (see [below for nested schema](#nestedblock--timeouts))",
		"",
		"* **Kalın giriş.** Sonra gerisi.",
		"",
		"~> Satıra sığmayan bir uyarı.",
		"",
		"| Simge | `+` |",
		"|--------|-----|",
		"",
		"```terraform",
		"# Önce makineyi oluşturun.",
		`resource "dtcloud_vm" "web" {`,
		"  # ...",
		"}",
		"```",
	}, "\n")

	if got := g.translate("resources/volume", body); got != want {
		t.Errorf("translate:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	if n := len(g.cat.missing); n != 0 {
		t.Errorf("unexpected missing texts: %v", g.cat.missing)
	}
}

func TestMissingTextFallsBackAndIsReported(t *testing.T) {
	g := testGenerator(map[string]string{
		"Changing this forces a new resource to be created.": "Bu değiştirilirse yeni bir kaynak oluşturulur.",
	})
	body := "- `name` (String) Untranslated. Changing this forces a new resource to be created."

	if got := g.translate("resources/x", body); got != body {
		t.Errorf("a missing translation must leave the English as it is, got %q", got)
	}
	// Only the hand-written half is missing; the generated note is known.
	if got := g.cat.missing["resources/x"]; len(got) != 1 || got[0] != "Untranslated." {
		t.Errorf("missing = %v, want [Untranslated.]", got)
	}

	var report strings.Builder
	if n := g.cat.report(&report); n != 1 {
		t.Errorf("report counted %d problems, want 1:\n%s", n, report.String())
	}
}

func TestPlaceholdersAreNotProse(t *testing.T) {
	g := testGenerator(nil)
	body := "```shell\n# <vm-id>:<port-id>\nterraform import dtcloud_vm.web 1234\n```"
	if got := g.translate("resources/vm", body); got != body {
		t.Errorf("got %q", got)
	}
	if len(g.cat.missing) != 0 {
		t.Errorf("a placeholder comment must not count as missing: %v", g.cat.missing)
	}
}
