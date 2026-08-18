# All binaries that touch Virtualization.framework must be signed with
# vz.entitlements (com.apple.security.virtualization); plain `go run` won't boot VMs.

BIN := bin
AGENT := internal/initramfs/shedguest_linux_arm64

.PHONY: build agent test clean

build: agent
	go build -o $(BIN)/shedd ./cmd/shedd
	codesign --entitlements vz.entitlements -f -s - $(BIN)/shedd
	go build -o $(BIN)/shed ./cmd/shed

agent:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(AGENT) ./cmd/shedguest

test: agent
	go test ./...

clean:
	rm -rf $(BIN) $(AGENT)
