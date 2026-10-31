VERSION ?= 0.1.0
LDFLAGS := -s -w

.PHONY: all agent relay dist fmt vet test clean

all: agent relay

agent:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/omenight-agent ./cmd/omenight-agent

relay:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/omenight-relay ./cmd/omenight-relay

# The node is aarch64 Alpine; the relay is x86_64 Debian.
dist:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/omenight-agent-arm64 ./cmd/omenight-agent
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/omenight-relay-amd64 ./cmd/omenight-relay

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -rf bin dist
