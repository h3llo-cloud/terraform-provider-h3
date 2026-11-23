default: build

build:
	go build -o terraform-provider-h3

install: build
	mkdir -p ~/go/bin
	cp terraform-provider-h3 ~/go/bin/terraform-provider-h3

# Build into bin/ for h3-tf-full-test dev_overrides (terraform-dev.tfrc).
install-dev: build
	mkdir -p bin
	cp terraform-provider-h3 bin/terraform-provider-h3

test:
	go test -v ./...

fmt:
	go fmt ./...

.PHONY: build install install-dev test fmt
# Regenerate docs/ from the schema, templates/ and examples/ (CI checks they are up to date).
docs:
	@command -v tfplugindocs >/dev/null || { echo "go install github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@latest"; exit 1; }
	tfplugindocs generate --provider-name h3 --rendered-provider-name "h3llo cloud"

# Fake-API acceptance tests (terraform-plugin-testing, needs terraform in PATH).
testacc:
	go test -count=1 ./internal/acctest/...

validate-examples:
	scripts/validate-examples.sh terraform

.PHONY: docs testacc validate-examples
