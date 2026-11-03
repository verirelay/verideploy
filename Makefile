.PHONY: build release test lint check clean

DIST_DIR := dist
VERIDEPLOY := $(DIST_DIR)/verideploy
VERSION ?= v0.0.0-dev
NFPM := go run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0

build:
	mkdir -p $(DIST_DIR)
	go build -o $(VERIDEPLOY) ./cmd/verideploy

release:
	rm -rf $(DIST_DIR)/release
	mkdir -p $(DIST_DIR)/release
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o $(DIST_DIR)/release/verideploy-linux-amd64 ./cmd/verideploy
	VERSION=$(VERSION) $(NFPM) package --config nfpm.yaml --packager deb --target $(DIST_DIR)/release/verideploy_amd64.deb
	VERSION=$(VERSION) $(NFPM) package --config nfpm.yaml --packager rpm --target $(DIST_DIR)/release/verideploy_x86_64.rpm
	cd $(DIST_DIR)/release && sha256sum verideploy* > SHA256SUMS

test:
	go test ./...

lint:
	golangci-lint run

check: test build lint

clean:
	rm -rf $(DIST_DIR)
