//go:build !linux

package main

import (
	"fmt"
	"os"
)

// shedguest only runs inside a Linux VM; this stub exists so `go build ./...`
// works on the macOS host.
func main() {
	fmt.Fprintln(os.Stderr, "shedguest is a Linux guest binary; build with GOOS=linux GOARCH=arm64 (make agent)")
	os.Exit(1)
}
