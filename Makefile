# Build helper for the anyrouter CPA plugin.
#
# The plugin is a native dynamic library, so it needs cgo and a C toolchain.
# On Windows specifically, do NOT rely on the first gcc in PATH: TDM-GCC 10.3.0
# links c-shared output that Windows refuses to load (ERROR_BAD_EXE_FORMAT).
# D:/Code/mingw64 (MinGW-W64 11.3.0, Brecht Sanders) is known good on this
# machine, and scripts/build.ps1 verifies loadability after every build.

PLUGIN_ID := anyrouter
GOOS      := $(shell go env GOOS)
EXT       := $(if $(filter windows,$(GOOS)),dll,$(if $(filter darwin,$(GOOS)),dylib,so))
DIST      := dist
BIN       := $(DIST)/$(PLUGIN_ID).$(EXT)
LDFLAGS   := -s -w
BUILDFLAGS := -buildmode=c-shared -trimpath

.PHONY: all build test vet fmt tidy package clean

all: build

build:
	CGO_ENABLED=1 go build $(BUILDFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN) .

# Unit tests run without cgo: cabi.go is implicitly cgo-only, so the package
# still compiles and the pure logic (config, challenge solving, classification)
# stays testable on a machine without a working C toolchain.
test:
	CGO_ENABLED=0 go test -count=1 ./...

vet:
	CGO_ENABLED=0 go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

# Store artifact: <pluginID>_<version>_<goos>_<goarch>.zip with the library at the
# archive root, plus checksums.txt.
package: build
	python scripts/build_release.py

clean:
	rm -rf $(DIST)
