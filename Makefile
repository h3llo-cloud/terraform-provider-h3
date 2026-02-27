default: build

build:
	go build -o terraform-provider-h3

install: build
	mkdir -p ~/go/bin
	cp terraform-provider-h3 ~/go/bin/terraform-provider-h3

test:
	go test -v ./...

fmt:
	go fmt ./...

.PHONY: build install test fmt