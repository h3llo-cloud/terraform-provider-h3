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