# All binaries that touch Virtualization.framework must be signed with
# vz.entitlements (com.apple.security.virtualization); plain `go run` won't boot VMs.

BIN := bin
AGENT := internal/initramfs/shedguest_linux_arm64

.PHONY: build spike agent test integration clean

build: agent
	go build -o $(BIN)/shedd ./cmd/shedd
	codesign --entitlements vz.entitlements -f -s - $(BIN)/shedd

agent:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(AGENT) ./cmd/shedguest

spike: agent
	go build -o $(BIN)/spike ./cmd/spike
	codesign --entitlements vz.entitlements -f -s - $(BIN)/spike

test: agent
	go test ./...

integration: build
	SHED_VZ_TESTS=1 go test -tags vzintegration -p 1 -exec "go run ./cmd/codesign" ./...

clean:
	rm -rf $(BIN) $(AGENT)
