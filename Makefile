VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/happyarch/stigmergy/internal/cli.Version=$(VERSION)

# CGO_ENABLED=0 is not incidental: the whole point of the pure-Go SQLite driver
# is a single static binary that runs anywhere without a libsqlite3 to match.
export CGO_ENABLED = 0

.PHONY: build test check install clean

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/stigmergy ./cmd/stigmergy

test:
	go test ./... -count=1

check: test
	gofmt -l . | grep . && { echo "gofmt: files above need formatting"; exit 1; } || true
	go vet ./...

install: build
	install -Dm755 bin/stigmergy $(DESTDIR)/usr/local/bin/stigmergy

clean:
	rm -rf bin
