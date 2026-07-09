package main

import (
	"errors"
	"strings"
	"testing"
)

type fakeLookup struct {
	byNameMap map[string]*serviceUser
	byUIDMap  map[int]*serviceUser
}

func (f fakeLookup) byName(n string) (*serviceUser, error) {
	if u, ok := f.byNameMap[n]; ok {
		return u, nil
	}
	return nil, errors.New("no such user")
}

func (f fakeLookup) byUID(uid int) (*serviceUser, error) {
	if u, ok := f.byUIDMap[uid]; ok {
		return u, nil
	}
	return nil, errors.New("no such uid")
}

func mkUser(name string, uid int) *serviceUser {
	return &serviceUser{Name: name, UID: uid, GID: uid, Group: name, Home: "/home/" + name}
}

func TestResolveServiceUserExplicit(t *testing.T) {
	lk := fakeLookup{byNameMap: map[string]*serviceUser{"deploy": mkUser("deploy", 1001)}}
	got, err := resolveServiceUser("deploy", lk)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Name != "deploy" {
		t.Fatalf("got %q, want deploy", got.Name)
	}
}

func TestResolveServiceUserExplicitRejectsRoot(t *testing.T) {
	lk := fakeLookup{byNameMap: map[string]*serviceUser{"root": mkUser("root", 0)}}
	if _, err := resolveServiceUser("root", lk); err == nil {
		t.Fatal("expected error for --service-user root")
	}
}

func TestResolveServiceUserExplicitNotFound(t *testing.T) {
	lk := fakeLookup{byNameMap: map[string]*serviceUser{}}
	if _, err := resolveServiceUser("ghost", lk); err == nil {
		t.Fatal("expected error for nonexistent --service-user")
	}
}

func TestResolveServiceUserPrefersListOrder(t *testing.T) {
	// Both ubuntu and debian exist; ubuntu comes first in the candidate list.
	lk := fakeLookup{byNameMap: map[string]*serviceUser{
		"debian": mkUser("debian", 1000),
		"ubuntu": mkUser("ubuntu", 1000),
	}}
	got, err := resolveServiceUser("", lk)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "ubuntu" {
		t.Fatalf("got %q, want ubuntu (first in candidate list)", got.Name)
	}
}

func TestResolveServiceUserPicksExistingFromList(t *testing.T) {
	lk := fakeLookup{byNameMap: map[string]*serviceUser{"centos": mkUser("centos", 1000)}}
	got, err := resolveServiceUser("", lk)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "centos" {
		t.Fatalf("got %q, want centos", got.Name)
	}
}

func TestResolveServiceUserFallsBackToUID1000(t *testing.T) {
	lk := fakeLookup{
		byNameMap: map[string]*serviceUser{}, // no known names
		byUIDMap:  map[int]*serviceUser{1000: mkUser("custom", 1000)},
	}
	got, err := resolveServiceUser("", lk)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "custom" {
		t.Fatalf("got %q, want custom (UID 1000 fallback)", got.Name)
	}
}

func TestResolveServiceUserErrorsWhenNothing(t *testing.T) {
	lk := fakeLookup{byNameMap: map[string]*serviceUser{}, byUIDMap: map[int]*serviceUser{}}
	if _, err := resolveServiceUser("", lk); err == nil {
		t.Fatal("expected error when no candidate user exists")
	}
}

func TestRenderSystemUnit(t *testing.T) {
	out, err := renderSystemUnit("/usr/local/bin/agbridge", "/home/ubuntu/.config/agbridge", mkUser("ubuntu", 1000))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"User=ubuntu",
		"Group=ubuntu",
		"WantedBy=multi-user.target",
		`ExecStart="/usr/local/bin/agbridge" daemon --state-dir "/home/ubuntu/.config/agbridge"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("system unit missing %q\n%s", want, out)
		}
	}
}

func TestRenderUserUnitHasNoUserDirective(t *testing.T) {
	out, err := renderUserUnit("/home/me/bin/agbridge", "/home/me/.config/agbridge")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "User=") {
		t.Errorf("user unit must not set User=\n%s", out)
	}
	if !strings.Contains(out, "WantedBy=default.target") {
		t.Errorf("user unit should target default.target\n%s", out)
	}
}

func TestRenderUnitRejectsNewlines(t *testing.T) {
	if _, err := renderSystemUnit("/bin/agbridge\nExecStartPre=/evil", "/x", mkUser("ubuntu", 1000)); err == nil {
		t.Fatal("expected newline rejection in system unit")
	}
	if _, err := renderUserUnit("/bin/agbridge", "/x\nEvil=1"); err == nil {
		t.Fatal("expected newline rejection in user unit")
	}
}

func TestSystemdQuoteArgEscapesSpecials(t *testing.T) {
	got := systemdQuoteArg(`/tmp/ag bridge/%bin"`)
	want := `"/tmp/ag bridge/%%bin\""`
	if got != want {
		t.Fatalf("systemdQuoteArg = %q, want %q", got, want)
	}
}

func TestShellQuoteArg(t *testing.T) {
	got := shellQuoteArg(`/tmp/ag bridge/it's`)
	want := `'/tmp/ag bridge/it'\''s'`
	if got != want {
		t.Fatalf("shellQuoteArg = %q, want %q", got, want)
	}
}
