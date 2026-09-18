BINARY := cli-proxy
GOFLAGS ?=
LDFLAGS := -s -w
VERSION := $(shell sed -n 's/^const version = "\(.*\)"/\1/p' main.go)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64
RELDIR := dist/$(BINARY)_$(VERSION)

.PHONY: all build slim test vet fmt run clean cross release

all: build

build:
	go build $(GOFLAGS) -o $(BINARY) .

# Stripped single binary, the way it is meant to be shipped.
slim:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

run: build
	./$(BINARY)

cross:
	mkdir -p dist
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 .
	GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 .
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 .
	GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-darwin-amd64 .
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe .

# Build the archives attached to a GitHub release, plus checksums.
# CI uses GoReleaser for this; the target is kept so a release can be reproduced
# locally without installing anything.
release: clean
	rm -rf $(RELDIR)
	@for target in $(PLATFORMS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		dir=$(RELDIR)/$$os-$$arch; \
		mkdir -p $$dir; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags="$(LDFLAGS)" -o $$dir/$(BINARY)$$ext . || exit 1; \
		cp README.md LICENSE $$dir/; \
		if [ "$$os" = windows ]; then \
			(cd $$dir && zip -q ../$(BINARY)_$(VERSION)_$${os}_$${arch}.zip $(BINARY).exe README.md LICENSE); \
		else \
			tar -czf $(RELDIR)/$(BINARY)_$(VERSION)_$${os}_$${arch}.tar.gz -C $$dir .; \
		fi; \
		rm -rf $$dir; \
	done
	@cd $(RELDIR) && if command -v shasum >/dev/null 2>&1; then shasum -a 256 * > checksums.txt; else sha256sum * > checksums.txt; fi
	@echo; echo "release artifacts in $(RELDIR):"; ls -1 $(RELDIR)

clean:
	rm -f $(BINARY)
	rm -rf dist
