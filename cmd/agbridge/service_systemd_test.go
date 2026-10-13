package main

import (
	"errors"
	"os"
	"path/filepath"
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

func TestPlanDaemonServiceRootDefaultsStateToUserHome(t *testing.T) {
	lk := fakeLookup{byNameMap: map[string]*serviceUser{"ubuntu": mkUser("ubuntu", 1000)}}
	// root, --state-dir NOT given → state goes under the service user's home.
	p, err := planDaemonService(0, "", "", false, lk)
	if err != nil {
		t.Fatal(err)
	}
	if !p.System || p.User == nil || p.User.Name != "ubuntu" {
		t.Fatalf("expected system mode as ubuntu, got %+v", p)
	}
	if p.StateDir != "/home/ubuntu/.config/agbridge" {
		t.Fatalf("state dir = %q, want /home/ubuntu/.config/agbridge (NOT /root)", p.StateDir)
	}
}

func TestPlanDaemonServiceRootKeepsExplicitStateDir(t *testing.T) {
	lk := fakeLookup{byNameMap: map[string]*serviceUser{"ubuntu": mkUser("ubuntu", 1000)}}
	p, err := planDaemonService(0, "", "/data/agb", true, lk)
	if err != nil {
		t.Fatal(err)
	}
	if p.StateDir != "/data/agb" {
		t.Fatalf("explicit state dir overwritten: %q", p.StateDir)
	}
}

func TestPlanDaemonServiceRootNoUserErrors(t *testing.T) {
	lk := fakeLookup{byNameMap: map[string]*serviceUser{}, byUIDMap: map[int]*serviceUser{}}
	p, err := planDaemonService(0, "", "", false, lk)
	if err == nil {
		t.Fatal("expected error when no service user resolvable")
	}
	if !p.System {
		t.Fatalf("plan should still mark system mode, got %+v", p)
	}
}

func TestPlanDaemonServiceNonRootIsUserMode(t *testing.T) {
	lk := fakeLookup{}
	p, err := planDaemonService(1000, "", "/home/me/.config/agbridge", true, lk)
	if err != nil {
		t.Fatal(err)
	}
	if p.System {
		t.Fatalf("non-root should be user mode, got %+v", p)
	}
	if p.StateDir != "/home/me/.config/agbridge" {
		t.Fatalf("user-mode state dir changed: %q", p.StateDir)
	}
}

func TestEnsureNoSymlinkBelowHomeCleanTree(t *testing.T) {
	home := t.TempDir()
	stateDir := filepath.Join(home, ".config", "agbridge")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := ensureNoSymlinkBelowHome(stateDir, home); err != nil {
		t.Fatalf("expected nil for clean real tree, got %v", err)
	}
}

func TestEnsureNoSymlinkBelowHomeRejectsSymlinkComponent(t *testing.T) {
	home := t.TempDir()
	elsewhere := t.TempDir()
	link := filepath.Join(home, ".config")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatalf("setup symlink: %v", err)
	}
	stateDir := filepath.Join(home, ".config", "agbridge")
	err := ensureNoSymlinkBelowHome(stateDir, home)
	if err == nil {
		t.Fatal("expected error for symlinked path component, got nil")
	}
	if !strings.Contains(err.Error(), link) {
		t.Fatalf("error should mention the symlink path %q, got: %v", link, err)
	}
}

func TestEnsureNoSymlinkBelowHomeIgnoresSymlinkAtOrAboveHome(t *testing.T) {
	realHome := t.TempDir()
	parent := t.TempDir()
	homeLink := filepath.Join(parent, "home-link")
	if err := os.Symlink(realHome, homeLink); err != nil {
		t.Fatalf("setup symlink: %v", err)
	}
	stateDir := filepath.Join(homeLink, "x")
	if err := os.MkdirAll(filepath.Join(realHome, "x"), 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := ensureNoSymlinkBelowHome(stateDir, homeLink); err != nil {
		t.Fatalf("home itself being a symlink should not be checked, got %v", err)
	}

	// stateDir not under home at all → nil.
	other := t.TempDir()
	if err := ensureNoSymlinkBelowHome(other, realHome); err != nil {
		t.Fatalf("stateDir outside home should be nil, got %v", err)
	}
}

func TestEnsureNoSymlinkBelowHomeNonexistentTail(t *testing.T) {
	home := t.TempDir()
	stateDir := filepath.Join(home, ".config", "agbridge")
	if err := ensureNoSymlinkBelowHome(stateDir, home); err != nil {
		t.Fatalf("nonexistent tail components should not error, got %v", err)
	}
}
