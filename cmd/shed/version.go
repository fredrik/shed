package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	gossh "golang.org/x/crypto/ssh"

	"github.com/fredrik/shed/internal/version"
)

// isVersionCmd reports whether argv asks for the version. This is the one
// command the client handles itself: it has its own version to report and
// must still answer when the daemon is down.
func isVersionCmd(args []string) bool {
	return len(args) > 0 && (args[0] == "version" || args[0] == "--version")
}

// runVersion prints client and daemon versions side by side. `dial` may
// return an error (daemon not running); the client version is printed
// regardless.
func runVersion(args []string, dial func() (*gossh.Client, error), stdout, stderr io.Writer) int {
	asJSON := slices.Contains(args[1:], "--json")
	me := version.Current()

	var daemon *version.Info
	var daemonErr error
	client, err := dial()
	if err != nil {
		daemonErr = err
	} else {
		defer client.Close()
		daemon, daemonErr = daemonVersion(client)
	}

	out, warn := formatVersion(me, daemon, daemonErr, asJSON)
	fmt.Fprint(stdout, out)
	if warn != "" {
		fmt.Fprintln(stderr, "shed:", warn)
	}
	return 0
}

func daemonVersion(client *gossh.Client) (*version.Info, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var buf bytes.Buffer
	sess.Stdout = &buf
	sess.Stderr = io.Discard
	if err := sess.Run("version --json"); err != nil {
		// An older daemon without the command: still running, version unknown.
		return nil, fmt.Errorf("daemon predates `version` (%v)", err)
	}
	var info version.Info
	if err := json.Unmarshal(buf.Bytes(), &info); err != nil {
		return nil, fmt.Errorf("parse daemon version: %v", err)
	}
	return &info, nil
}

// formatVersion renders the report; warn is non-empty when the client and
// daemon builds differ (stale daemon still running after a rebuild).
func formatVersion(me version.Info, daemon *version.Info, daemonErr error, asJSON bool) (out, warn string) {
	if daemon != nil && *daemon != me {
		warn = fmt.Sprintf("client is %s but daemon is %s; restart shedd", me, *daemon)
	}
	if asJSON {
		report := struct {
			Shed  version.Info  `json:"shed"`
			Shedd *version.Info `json:"shedd"`
			Error string        `json:"shedd_error,omitempty"`
		}{Shed: me, Shedd: daemon}
		if daemonErr != nil {
			report.Error = daemonErr.Error()
		}
		b, _ := json.MarshalIndent(report, "", "  ")
		return string(b) + "\n", warn
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "shed  %s\n", me)
	switch {
	case daemon != nil:
		fmt.Fprintf(&sb, "shedd %s\n", *daemon)
	default:
		fmt.Fprintf(&sb, "shedd unknown: %v\n", daemonErr)
	}
	return sb.String(), warn
}
