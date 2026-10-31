VERSION ?= dev
LDFLAGS := -s -w

.PHONY: all relay dist fmt vet test e2e clean

all: relay

relay:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/omenight-relay ./cmd/omenight-relay

dist:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/omenight-relay-linux-amd64 ./cmd/omenight-relay
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/omenight-relay-linux-arm64 ./cmd/omenight-relay

fmt:
	gofmt -l -w .

vet:
	go vet ./...

test:
	go test ./...

# Real kernel, unprivileged. Needs wg, wg-quick, nft and python3.
e2e:
	./e2e/netns.sh

clean:
	rm -rf bin dist
