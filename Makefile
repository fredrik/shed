# All binaries that touch Virtualization.framework must be signed with
# vz.entitlements (com.apple.security.virtualization); plain `go run` won't boot VMs.

BIN := bin
AGENT := internal/initramfs/shedguest_linux_arm64

.PHONY: build agent test clean kernel

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

# Build the guest kernel inside a throwaway VM on the running daemon; see
# kernel/README.md. Output: $(BIN)/kernel/{Image,Image.sha256,config}.
# The VM is removed whether or not the build succeeds.
SHED ?= $(BIN)/shed
SHED_HOST ?= shed
SHED_SSH ?= ssh
SHED_SCP ?= scp
KERNEL_VM ?= kernel-build
KERNEL_CPUS ?= 8

kernel: build
	$(SHED) new $(KERNEL_VM) --cpu $(KERNEL_CPUS) --memory 8192 --disk 20
	trap '$(SHED) rm $(KERNEL_VM)' EXIT; set -e; \
	$(SHED_SCP) -r kernel $(KERNEL_VM)@$(SHED_HOST):; \
	$(SHED_SSH) $(KERNEL_VM)@$(SHED_HOST) ./kernel/build.sh; \
	mkdir -p $(BIN)/kernel; \
	$(SHED_SCP) '$(KERNEL_VM)@$(SHED_HOST):kernel-out/*' $(BIN)/kernel/
	@echo; echo "pin in internal/kernel/kernel.go:  imageSHA256 = \"$$(cat $(BIN)/kernel/Image.sha256)\""
