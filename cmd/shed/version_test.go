package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fredrik/shed/internal/version"
)

var me = version.Info{Version: "v0.2.0", Commit: "abc1234", Go: "go1.25"}

func TestIsVersionCmd(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"version", "--json"}, {"--version"}} {
		if !isVersionCmd(args) {
			t.Errorf("%v should be a version command", args)
		}
	}
	for _, args := range [][]string{{}, {"ls"}, {"new", "version"}} {
		if isVersionCmd(args) {
			t.Errorf("%v should not be a version command", args)
		}
	}
}

func TestFormatVersionMatching(t *testing.T) {
	d := me
	out, warn := formatVersion(me, &d, nil, false)
	if out != "shed  v0.2.0 (abc1234, go1.25)\nshedd v0.2.0 (abc1234, go1.25)\n" {
		t.Fatalf("out: %q", out)
	}
	if warn != "" {
		t.Fatalf("unexpected warning: %q", warn)
	}
}

func TestFormatVersionMismatchWarns(t *testing.T) {
	d := version.Info{Version: "v0.1.0", Commit: "0000000", Go: "go1.25"}
	_, warn := formatVersion(me, &d, nil, false)
	if !strings.Contains(warn, "v0.1.0") || !strings.Contains(warn, "restart shedd") {
		t.Fatalf("warn: %q", warn)
	}
}

func TestFormatVersionDaemonDown(t *testing.T) {
	out, warn := formatVersion(me, nil, errors.New("connection refused"), false)
	if !strings.HasPrefix(out, "shed  v0.2.0") || !strings.Contains(out, "shedd unknown: connection refused") {
		t.Fatalf("out: %q", out)
	}
	if warn != "" {
		t.Fatalf("no warning expected when daemon is down: %q", warn)
	}
}

func TestFormatVersionJSON(t *testing.T) {
	out, _ := formatVersion(me, nil, errors.New("down"), true)
	var got struct {
		Shed  version.Info  `json:"shed"`
		Shedd *version.Info `json:"shedd"`
		Error string        `json:"shedd_error"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if got.Shed != me || got.Shedd != nil || got.Error != "down" {
		t.Fatalf("got %+v", got)
	}
}
