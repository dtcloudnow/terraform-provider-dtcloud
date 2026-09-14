#!/usr/bin/env bash
#
# Regenerate the Terraform documentation and open a merge request against the
# Docusaurus site with the result.
#
# This mirrors dt-cli's scripts/ci/docs-mr.sh so both products publish the same
# way, with one difference: the CLI generates Docusaurus pages directly, while
# the provider generates Registry pages into docs/ first and converts those. The
# Registry copy is the canonical one, so this script regenerates it and refuses
# to continue if it does not match what is committed -- a docs update must come
# from a `make docs` the author actually ran, not from CI quietly fixing it up.
#
# Required CI variables:
#   DOCS_PROJECT_PATH  namespace/project of the Docusaurus repo
#   DOCS_BOT_TOKEN     token with write access to it
#   DOCS_BOT_NAME      commit author name
#   DOCS_BOT_EMAIL     commit author email
# Optional:
#   DOCS_TARGET_BRANCH branch to target (default: the site's default branch)
#   DOCS_SLUG          folder inside the site's docs tree (default: terraform)

set -euo pipefail

: "${DOCS_PROJECT_PATH:?DOCS_PROJECT_PATH is not set}"
: "${DOCS_BOT_TOKEN:?DOCS_BOT_TOKEN is not set}"
: "${DOCS_BOT_NAME:?DOCS_BOT_NAME is not set}"
: "${DOCS_BOT_EMAIL:?DOCS_BOT_EMAIL is not set}"

GITLAB_HOST="${CI_SERVER_HOST:?CI_SERVER_HOST is not set}"
PROVIDER_DIR="${CI_PROJECT_DIR:-$(pwd)}"
SHORT_SHA="${CI_COMMIT_SHORT_SHA:-$(git -C "$PROVIDER_DIR" rev-parse --short HEAD 2>/dev/null || echo local)}"
REF_NAME="${CI_COMMIT_REF_NAME:-local}"
REF_SLUG="${CI_COMMIT_REF_SLUG:-local}"
SLUG="${DOCS_SLUG:-terraform}"

if [ ! -d "$PROVIDER_DIR/cmd/gendoc" ]; then
  echo "ERROR: $PROVIDER_DIR/cmd/gendoc not found: this branch does not carry the docs converter." >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# 1. The Registry docs must already be up to date.
# ---------------------------------------------------------------------------
echo "==> regenerating docs/ and checking it against the commit"
cd "$PROVIDER_DIR"
make docs
if ! git diff --quiet -- docs/; then
  echo "ERROR: docs/ does not match the provider schema on this commit." >&2
  echo "Run 'make docs' locally and commit the result. Diff:" >&2
  git --no-pager diff --stat -- docs/ >&2
  exit 1
fi
make docs_validate
# The Turkish pages fall back to English for untranslated text; refuse to publish that.
make docs_i18n_check

# ---------------------------------------------------------------------------
# 2. Clone the site.
# ---------------------------------------------------------------------------
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
DOCS_DIR="$WORK/docs-site"

echo "==> cloning ${DOCS_PROJECT_PATH} from ${GITLAB_HOST}"
CLONE_ARGS=(--depth 1)
if [ -n "${DOCS_TARGET_BRANCH:-}" ]; then
  CLONE_ARGS+=(-b "$DOCS_TARGET_BRANCH")
fi

export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0="http.extraHeader"
export GIT_CONFIG_VALUE_0="Authorization: Basic $(printf 'oauth2:%s' "$DOCS_BOT_TOKEN" | base64 | tr -d '\n')"
git clone "${CLONE_ARGS[@]}" \
  "https://${GITLAB_HOST}/${DOCS_PROJECT_PATH}.git" "$DOCS_DIR"
TARGET_BRANCH="${DOCS_TARGET_BRANCH:-$(git -C "$DOCS_DIR" rev-parse --abbrev-ref HEAD)}"
echo "==> MR target branch: ${TARGET_BRANCH}"

# ---------------------------------------------------------------------------
# 3. Convert docs/ into the site (EN + TR).
# ---------------------------------------------------------------------------
echo "==> converting the Terraform reference (EN + TR)"
go run ./cmd/gendoc --out "$DOCS_DIR" --lang en --slug "$SLUG"
go run ./cmd/gendoc --out "$DOCS_DIR" --lang tr --slug "$SLUG"

cd "$DOCS_DIR"
git config core.quotePath false

if [ -z "$(git status --porcelain)" ]; then
  echo "==> no changes: the site already matches terraform-provider-dtcloud@${SHORT_SHA}, nothing to do."
  exit 0
fi

# ---------------------------------------------------------------------------
# 4. Refuse to touch anything outside our own folders.
# ---------------------------------------------------------------------------
echo "==> path gate: changes must stay inside the ${SLUG} doc folders"
ALLOWED="^(docs/${SLUG}(/|\$)|i18n/tr/docusaurus-plugin-content-docs/current/${SLUG}(/|\$))"
VIOLATIONS="$(git status --porcelain | cut -c4- | grep -Ev "$ALLOWED" || true)"
if [ -n "$VIOLATIONS" ]; then
  echo "ERROR: the converter touched paths outside the ${SLUG} folders:" >&2
  echo "$VIOLATIONS" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# 5. The site must still build. onBrokenLinks is 'throw', so a bad generated
#    link fails here rather than on the live site.
# ---------------------------------------------------------------------------
echo "==> validating the docs site build"
if ! npm ci --no-audit --no-fund; then
  echo "WARN: npm ci failed: package-lock.json is likely out of sync with package.json." >&2
  echo "WARN: falling back to 'npm install'; fix the lockfile in the docs repo." >&2
  npm install --no-audit --no-fund
fi
npm run build

# ---------------------------------------------------------------------------
# 6. Commit and open (or update) the merge request.
# ---------------------------------------------------------------------------
echo "==> committing and opening MR"
BOT_BRANCH="bot/terraform-docs-${REF_SLUG}"
git config user.name "$DOCS_BOT_NAME"
git config user.email "$DOCS_BOT_EMAIL"
git checkout -b "$BOT_BRANCH"
git add -A -- "docs/${SLUG}" "i18n/tr/docusaurus-plugin-content-docs/current/${SLUG}"
git commit \
  -m "docs(terraform): regenerate the provider reference from terraform-provider-dtcloud@${SHORT_SHA}" \
  -m "Source: ${CI_PROJECT_URL:-terraform-provider-dtcloud}/-/commit/${CI_COMMIT_SHA:-$SHORT_SHA}"

# Force-push so a re-run updates the open MR instead of stacking a new one.
git push --force origin "$BOT_BRANCH" \
  -o merge_request.create \
  -o merge_request.target="$TARGET_BRANCH" \
  -o merge_request.title="docs(terraform): provider reference update (${REF_NAME})" \
  -o merge_request.description="Auto-generated from terraform-provider-dtcloud ${REF_NAME} @ ${SHORT_SHA}. Pipeline: ${CI_PIPELINE_URL:-manual run}. Only docs/${SLUG} and the Turkish i18n mirror change; the site build was validated with onBrokenLinks:'throw'." \
  -o merge_request.remove_source_branch

echo "==> done: pushed ${BOT_BRANCH}; MR created (or updated if one was already open)."
