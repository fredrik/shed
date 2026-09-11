// Package control implements the SSH control plane: the exe.dev-style
// command surface reached via `ssh shed <command>`.
package control

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kballard/go-shellquote"

	"github.com/fredrik/shed/internal/config"
	"github.com/fredrik/shed/internal/httpgate"
	"github.com/fredrik/shed/internal/vm"
)

type Deps struct {
	Mgr                *vm.Manager
	Cfg                *config.Config
	Gate               *httpgate.Server
	AuthorizedKeysPath string
	User               string // authenticated local user, for whoami
}

// Session is the slice of gliderlabs/ssh.Session the control plane needs;
// tests can fake it.
type Session interface {
	RawCommand() string
	io.Reader
	io.Writer
	Stderr() io.ReadWriter
}

// Run parses the SSH exec line like a shell would and executes it against
// the cobra tree. Returns the session exit code.
func Run(sess Session, deps Deps) int {
	if isGhosttyTerminfoProbe(sess.RawCommand()) {
		// Ghostty pipes its terminfo on stdin; drain it so the client
		// sees a clean exit rather than a broken pipe.
		io.Copy(io.Discard, sess)
		return 0
	}
	argv, err := shellquote.Split(sess.RawCommand())
	if err != nil {
		fmt.Fprintf(sess.Stderr(), "shed: parse command: %v\n", err)
		return 1
	}
	if len(argv) == 0 {
		argv = []string{"help"}
	}
	root := newRoot(deps)
	root.SetArgs(argv)
	root.SetIn(sess)
	root.SetOut(sess)
	root.SetErr(sess.Stderr())
	if err := root.Execute(); err != nil {
		return 1
	}
	return 0
}

// isGhosttyTerminfoProbe reports whether cmd is the shell snippet Ghostty's
// ssh-terminfo integration runs before connecting to an uncached host:
// "infocmp xterm-ghostty ... || tic -x -". We are not a shell and cannot run
// it, so cobra would reject it and Ghostty would print "Warning: Failed to
// install terminfo." on every `ssh shed`.
//
// Exit 0 tells Ghostty that TERM=xterm-ghostty is fine here, and it caches
// the host. That is vacuously true: the control plane emits plain text and
// never consults TERM or terminfo. Nothing is installed.
func isGhosttyTerminfoProbe(cmd string) bool {
	return strings.Contains(cmd, "infocmp xterm-ghostty") && strings.Contains(cmd, "tic -x -")
}

func printJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func appendFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// table renders aligned columns.
func table(out io.Writer, headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	writeRow := func(cells []string) {
		var b strings.Builder
		for i, cell := range cells {
			if i > 0 {
				b.WriteString("  ")
			}
			if i == len(cells)-1 {
				b.WriteString(cell)
			} else {
				b.WriteString(cell + strings.Repeat(" ", widths[i]-len(cell)))
			}
		}
		fmt.Fprintln(out, strings.TrimRight(b.String(), " "))
	}
	writeRow(headers)
	for _, row := range rows {
		writeRow(row)
	}
}
