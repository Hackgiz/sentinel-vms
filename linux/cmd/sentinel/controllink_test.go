package main

import (
	"strings"
	"testing"
)

func TestControlListenDefaults(t *testing.T) {
	for _, tc := range []struct {
		role, value string
		set         bool
		want        string
	}{
		{roleManagement, "", false, ":8091"},
		{roleAll, "", false, ""},
		{roleRecording, "", false, ""},
		{roleAll, ":9000", true, ":9000"},
		{roleManagement, "off", true, ""},
	} {
		if got := controlListenFor(tc.role, tc.value, tc.set); got != tc.want {
			t.Errorf("%s %q %v: %q", tc.role, tc.value, tc.set, got)
		}
	}
}

func TestSameManagement(t *testing.T) {
	if !sameManagement("https://ms.lan:8091", "ms.lan") || !sameManagement("https://MS.lan:8091/", "https://ms.lan:8091") {
		t.Fatal("same")
	}
	if sameManagement("https://ms.lan:8091", "https://ms.lan:9000") || sameManagement("https://a:8091", "https://b:8091") {
		t.Fatal("different")
	}
}

func TestVideoURL(t *testing.T) {
	if got := videoURLFor("http://rs.lan:8090/", ":8090"); got != "http://rs.lan:8090" {
		t.Fatal(got)
	}
	if got := videoURLFor("", "10.0.0.5:8090"); got != "http://10.0.0.5:8090" {
		t.Fatal(got)
	}
	if got := videoURLFor("", ":8090"); !strings.HasPrefix(got, "http://") || !strings.HasSuffix(got, ":8090") {
		t.Fatal(got)
	}
	if got := splitList(" a.lan, 10.0.0.1 ,,b "); strings.Join(got, "|") != "a.lan|10.0.0.1|b" {
		t.Fatal(got)
	}
}
