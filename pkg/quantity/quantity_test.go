package quantity

import "testing"

func TestParseMB(t *testing.T) {
	mb, err := ParseMB("1400Mi")
	if err != nil || mb != 1400 {
		t.Fatalf("%v %v", mb, err)
	}
	gb, err := ParseMB("2Gi")
	if err != nil || gb != 2048 {
		t.Fatalf("%v %v", gb, err)
	}
	if _, err := ParseMB("2GB"); err == nil {
		t.Fatal("2GB should fail")
	}
}

func TestParseMS(t *testing.T) {
	ms, err := ParseMS("80ms")
	if err != nil || ms == nil || *ms != 80 {
		t.Fatalf("%v %v", ms, err)
	}
	empty, err := ParseMS("")
	if err != nil || empty != nil {
		t.Fatal("empty")
	}
}
