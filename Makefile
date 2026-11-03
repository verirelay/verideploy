.PHONY: build release test lint check clean

DIST_DIR := dist
DEPLOY := $(DIST_DIR)/deploy
VERSION ?= dev

build:
	mkdir -p $(DIST_DIR)
	go build -o $(DEPLOY) ./cmd/deploy

release:
	rm -rf $(DIST_DIR)/release
	mkdir -p $(DIST_DIR)/release
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o $(DIST_DIR)/release/deploy-linux-amd64 ./cmd/deploy
	cd $(DIST_DIR)/release && sha256sum deploy-* > SHA256SUMS

test:
	go test ./...

lint:
	golangci-lint run

check: test build lint

clean:
	rm -rf $(DIST_DIR)
