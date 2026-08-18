# All binaries that touch Virtualization.framework must be signed with
# vz.entitlements (com.apple.security.virtualization); plain `go run` won't boot VMs.

BIN := bin
AGENT := internal/initramfs/exeguest_linux_arm64

.PHONY: build spike agent test integration clean

build: agent
	go build -o $(BIN)/exed ./cmd/exed
	codesign --entitlements vz.entitlements -f -s - $(BIN)/exed

agent:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(AGENT) ./cmd/exeguest

spike: agent
	go build -o $(BIN)/spike ./cmd/spike
	codesign --entitlements vz.entitlements -f -s - $(BIN)/spike

test: agent
	go test ./...

integration: build
	DEVEXE_VZ_TESTS=1 go test -tags vzintegration -p 1 -exec "go run ./cmd/codesign" ./...

clean:
	rm -rf $(BIN) $(AGENT)
