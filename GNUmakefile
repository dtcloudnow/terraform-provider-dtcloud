NAME=dtcloud
BINARY=terraform-provider-${NAME}

default: build

# build installs the provider binary into $GOPATH/bin, where a ~/.terraformrc
# dev_overrides block can point Terraform at it.
build: fmtcheck
	go install

test:
	go test ./... -timeout=120s

# Real API acceptance tests. Requires DTCLOUD_ACCESS_KEY / DTCLOUD_SECRET_KEY /
# DTCLOUD_API_URL / DTCLOUD_REGION_ID to be set.
testacc:
	TF_ACC=1 go test -v ./dtcloud/... -timeout 120m

fmt:
	gofmt -w .

fmtcheck:
	@test -z "$$(gofmt -l . | grep -v vendor)" || (echo "run 'make fmt'"; exit 1)

vet:
	go vet ./...

# ---------------------------------------------------------------------------
# Documentation
#
# The chain is: the provider schema is the source of truth, tfplugindocs renders
# it into docs/ in Terraform Registry format, and gendoc re-frames docs/ for the
# Docusaurus site. Nothing under docs/ is edited by hand -- change a Description
# string, an example, or a template, and run `make docs`.
# ---------------------------------------------------------------------------

# TFPLUGINDOCS is pinned in tools/go.mod so every machine and every CI run
# generates with the same version.
TFPLUGINDOCS = cd tools && go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs

.PHONY: docs
docs:
	@echo "==> generate the Registry reference into docs/"
	@$(TFPLUGINDOCS) generate --provider-dir .. --provider-name $(NAME)

# docs_check is the drift gate: it regenerates and fails if the result differs
# from what is committed. This is what makes the docs track the code -- a schema
# change with no `make docs` behind it cannot reach the default branch.
.PHONY: docs_check
docs_check: docs
	@echo "==> ensure docs/ matches the provider schema"
	@git diff --exit-code -- docs/ || \
		(echo ""; echo "docs/ is out of date: run 'make docs' and commit the result"; exit 1)

.PHONY: docs_validate
docs_validate:
	@echo "==> validate docs/ against the Registry's rules"
	@$(TFPLUGINDOCS) validate --provider-dir .. --provider-name $(NAME)

# docs_i18n_check is the same gate for the Turkish site: it fails when a text in
# docs/ has no translation under i18n/tr, or a translation is no longer used, and
# prints the missing text as YAML ready to be filled in.
.PHONY: docs_i18n_check
docs_i18n_check:
	@echo "==> ensure every text in docs/ has a Turkish translation"
	@go run ./cmd/gendoc --lang tr --check

# DOCS_OUT is the Docusaurus site root the reference is written into. Defaults
# to a sibling checkout, matching the dt-cli setup.
DOCS_OUT = $(shell echo $${DOCS_OUT:-$(CURDIR)/../docusaurus})

.PHONY: docusaurus
docusaurus:
	@echo "==> convert docs/ into the Docusaurus site at $(DOCS_OUT) (en + tr)"
	@go run ./cmd/gendoc --out "$(DOCS_OUT)" --lang en
	@go run ./cmd/gendoc --out "$(DOCS_OUT)" --lang tr

# Everything, in order: what CI runs before opening the docs merge request.
.PHONY: docs_all
docs_all: docs docs_validate docs_i18n_check docusaurus

.PHONY: default build test testacc fmt fmtcheck vet
