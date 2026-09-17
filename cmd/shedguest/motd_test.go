package main

import "testing"

func TestRenderMOTDFillsPlaceholders(t *testing.T) {
	in := []byte("  sheduntu -- Ubuntu 26.04, Linux <kernel>\n  http://<vmname>.shed.localhost:8080\n")
	got := string(renderMOTD(in, "ktest", "6.18.15-shed"))
	want := "  sheduntu -- Ubuntu 26.04, Linux 6.18.15-shed\n  http://ktest.shed.localhost:8080\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestRenderMOTDLeavesPlainTextAlone(t *testing.T) {
	in := []byte("welcome\n")
	if got := string(renderMOTD(in, "x", "y")); got != "welcome\n" {
		t.Fatalf("got %q", got)
	}
}
