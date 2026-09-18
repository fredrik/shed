package version

import (
	"encoding/json"
	"runtime/debug"
	"testing"
)

func TestResolvePrefersLinkerValue(t *testing.T) {
	got := resolve("v0.3.0-2-gabc1234-dirty", "abc1234", nil)
	if got.Version != "v0.3.0-2-gabc1234-dirty" || got.Commit != "abc1234" {
		t.Fatalf("resolve: %+v", got)
	}
	if got.Go == "" {
		t.Fatal("Go version must always be filled in")
	}
}

func TestResolveFallsBackToBuildInfo(t *testing.T) {
	bi := fakeBuildInfo("v0.2.0", "0123456789abcdef", "true")
	got := resolve("", "", bi)
	if got.Version != "v0.2.0" {
		t.Fatalf("version from build info: %q", got.Version)
	}
	if got.Commit != "0123456789ab-dirty" {
		t.Fatalf("commit from build info: %q", got.Commit)
	}
}

func TestResolveDevelWithoutInfo(t *testing.T) {
	got := resolve("", "", fakeBuildInfo("(devel)", "", ""))
	if got.Version != "dev" || got.Commit != "" {
		t.Fatalf("devel build: %+v", got)
	}
	if got := resolve("", "", nil); got.Version != "dev" {
		t.Fatalf("no build info: %+v", got)
	}
	pseudo := fakeBuildInfo("v0.0.0-20260918071511-2469bc317624+dirty", "2469bc317624abcd", "true")
	if got := resolve("", "", pseudo); got.Version != "dev" || got.Commit != "2469bc317624-dirty" {
		t.Fatalf("pseudo-version build: %+v", got)
	}
}

func TestStringFormats(t *testing.T) {
	cases := map[Info]string{
		{Version: "v0.1.0", Commit: "abc1234", Go: "go1.25"}: "v0.1.0 (abc1234, go1.25)",
		{Version: "dev", Go: "go1.25"}:                       "dev (go1.25)",
	}
	for in, want := range cases {
		if got := in.String(); got != want {
			t.Errorf("String(%+v) = %q, want %q", in, got, want)
		}
	}
}

func TestInfoJSONKeys(t *testing.T) {
	b, err := json.Marshal(Info{Version: "v1", Commit: "c", Go: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"version":"v1","commit":"c","go":"g"}` {
		t.Fatalf("json: %s", b)
	}
}

func fakeBuildInfo(mainVersion, revision, modified string) *debug.BuildInfo {
	bi := &debug.BuildInfo{}
	bi.Main.Version = mainVersion
	if revision != "" {
		bi.Settings = append(bi.Settings,
			debug.BuildSetting{Key: "vcs.revision", Value: revision},
			debug.BuildSetting{Key: "vcs.modified", Value: modified})
	}
	return bi
}
