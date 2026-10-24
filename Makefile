# SentinelProxy Makefile

.PHONY: all build test test-race vet clean demo

all: build test vet

build:
	go build -o bin/sentinelproxy.exe ./cmd/sentinelproxy
	go build -o bin/mock-backend.exe ./cmd/mock-backend
	go build -o bin/loadgen.exe ./cmd/loadgen

test:
	go test -v ./...

test-race:
	go test -race -v ./...

vet:
	go vet ./...

clean:
	go clean
	rm -rf bin/
