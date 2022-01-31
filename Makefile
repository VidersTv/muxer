all: build_deps linux

BUILDER := "unknown"
VERSION := "unknown"

ifeq ($(origin MUXER_BUILDER),undefined)
	BUILDER = $(shell git config --get user.name);
else
	BUILDER = ${MUXER_BUILDER};
endif

ifeq ($(origin MUXER_VERSION),undefined)
	VERSION = $(shell git rev-parse HEAD);
else
	VERSION = ${MUXER_VERSION};
endif

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -v -ldflags "-X 'main.Version=${VERSION}' -X 'main.Unix=$(shell date +%s)' -X 'main.User=${BUILDER}'" -o bin/muxer .
	
lint:
	staticcheck ./...
	go vet ./...
	golangci-lint run
	yarn prettier --write .

deps: go_installs
	go mod download
	yarn

build_deps:

go_installs: build_deps
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

test:
	go test -count=1 -cover ./...
