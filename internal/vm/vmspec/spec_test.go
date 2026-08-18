package vmspec

import "testing"

func TestValidName(t *testing.T) {
	for _, ok := range []string{"a", "box", "web-2", "a1-b2-c3"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "-x", "UPPER", "has space", "dot.name", "exe@", "a_b",
		"averyveryveryveryveryveryveryveryveryveryveryveryverylongname-that-exceeds"} {
		if err := ValidName(bad); err == nil {
			t.Errorf("ValidName(%q) = nil, want error", bad)
		}
	}
}

func TestMACStableAndLocal(t *testing.T) {
	a1 := Spec{Name: "box"}.MAC()
	a2 := Spec{Name: "box"}.MAC()
	b := Spec{Name: "web"}.MAC()
	if a1.String() != a2.String() {
		t.Fatal("MAC not deterministic")
	}
	if a1.String() == b.String() {
		t.Fatal("MAC collision between names")
	}
	if a1[0] != 0x06 {
		t.Fatalf("MAC not locally administered unicast: %s", a1)
	}
}
