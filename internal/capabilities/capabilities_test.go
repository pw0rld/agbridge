package capabilities

import "testing"

func TestRoundTrip(t *testing.T) {
	orig := Capabilities{
		DaemonName:        "lab01",
		DaemonHostname:    "host.local",
		AllowedExecCwds:   []string{"/foo", "/foo/*"},
		AllowedReadPaths:  []string{"/foo", "/foo/*"},
		AllowedWritePaths: []string{"/foo", "/foo/*"},
		EnvAllowlist:      []string{"PATH", "HOME"},
		ForbiddenPorts:    []int{22, 443},
		MaxFileBytes:      10 << 20,
		AgbridgeVersion:   "0.2.0",
	}
	b, err := orig.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DaemonName != orig.DaemonName || got.DaemonHostname != orig.DaemonHostname {
		t.Errorf("name/hostname lost: %+v", got)
	}
	if len(got.AllowedExecCwds) != 2 || got.AllowedExecCwds[1] != "/foo/*" {
		t.Errorf("exec cwds lost: %+v", got.AllowedExecCwds)
	}
	if got.MaxFileBytes != 10<<20 {
		t.Errorf("max_file_bytes: %d", got.MaxFileBytes)
	}
	if len(got.ForbiddenPorts) != 2 || got.ForbiddenPorts[0] != 22 {
		t.Errorf("forbidden_ports lost: %+v", got.ForbiddenPorts)
	}
}

func TestDecodeBadJSON(t *testing.T) {
	if _, err := Decode([]byte("not json")); err == nil {
		t.Fatal("expected decode error for garbage")
	}
}

func TestDecodeEmptySliceOmitted(t *testing.T) {
	c := Capabilities{DaemonName: "x"}
	b, _ := c.Encode()
	s := string(b)
	// env_allowlist/forbidden_ports have omitempty when empty
	for _, key := range []string{`"env_allowlist"`, `"forbidden_ports"`} {
		if contains(s, key) {
			t.Errorf("expected %s to be omitted when empty: %s", key, s)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
