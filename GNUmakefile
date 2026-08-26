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

.PHONY: default build test testacc fmt fmtcheck vet
