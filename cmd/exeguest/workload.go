//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/fredrik/local-devexe/internal/vsockproto"
)

// startWorkload supervises the image's ENTRYPOINT/CMD the way exe.dev runs
// a container image as a service. Bare interactive shells (the CMD of base
// images like alpine or ubuntu) are skipped — the VM is the product there,
// not the process.
func startWorkload(cfg *vsockproto.Config) {
	argv := append(slices.Clone(cfg.Entrypoint), cfg.Cmd...)
	if len(argv) == 0 || isBareShell(argv) {
		return
	}
	env := cfg.Env
	if !hasPath(env) {
		env = append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
	go superviseWorkload(argv, env, cfg.WorkingDir)
}

func superviseWorkload(argv, env []string, dir string) {
	backoff := time.Second
	for {
		fmt.Printf("exeguest: starting workload: %s\n", strings.Join(argv, " "))
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = env
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		start := time.Now()
		err := cmd.Run()
		fmt.Printf("exeguest: workload exited after %s: %v\n", time.Since(start).Round(time.Second), err)
		if time.Since(start) > time.Minute {
			backoff = time.Second
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
		time.Sleep(backoff)
	}
}

func isBareShell(argv []string) bool {
	if len(argv) != 1 {
		return false
	}
	switch argv[0] {
	case "/bin/sh", "/bin/bash", "sh", "bash", "/bin/ash", "/bin/dash", "/bin/zsh":
		return true
	}
	return false
}

func hasPath(env []string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			return true
		}
	}
	return false
}
