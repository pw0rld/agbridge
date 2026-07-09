package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const daemonSystemdServiceName = "agbridge-daemon.service"

// defaultServiceUserCandidates are common cloud/distro default login
// accounts. When enroll runs as root and no --service-user is given, the
// daemon runs as the first of these that exists (daemon refuses root).
var defaultServiceUserCandidates = []string{
	"ubuntu", "ec2-user", "centos", "debian", "rocky",
	"almalinux", "fedora", "admin", "cloud-user", "opc", "azureuser",
}

// serviceUser is a resolved non-root account the daemon service runs as.
type serviceUser struct {
	Name  string
	Home  string
	Group string
	UID   int
	GID   int
}

// userLookup abstracts account resolution so the detection logic is
// unit-testable without touching the real /etc/passwd.
type userLookup interface {
	byName(string) (*serviceUser, error)
	byUID(int) (*serviceUser, error)
}

// resolveServiceUser picks the non-root account the system service runs
// as: an explicit name if given, else the first known cloud/distro
// default that exists, else the UID-1000 account, else an error. It
// never creates a user.
func resolveServiceUser(explicit string, lk userLookup) (*serviceUser, error) {
	if explicit != "" {
		u, err := lk.byName(explicit)
		if err != nil {
			return nil, fmt.Errorf("--service-user %q: %w", explicit, err)
		}
		if u.UID == 0 {
			return nil, errors.New("--service-user must not be root (the daemon refuses to run as root)")
		}
		return u, nil
	}
	for _, name := range defaultServiceUserCandidates {
		if u, err := lk.byName(name); err == nil && u.UID != 0 {
			return u, nil
		}
	}
	if u, err := lk.byUID(1000); err == nil && u.UID != 0 {
		return u, nil
	}
	return nil, errors.New("could not detect a non-root service user; pass --service-user <name>")
}

// osUserLookup is the production userLookup backed by os/user (pure-Go
// /etc/passwd parser under CGO_ENABLED=0).
type osUserLookup struct{}

func (osUserLookup) byName(name string) (*serviceUser, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return nil, err
	}
	return toServiceUser(u)
}

func (osUserLookup) byUID(uid int) (*serviceUser, error) {
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return nil, err
	}
	return toServiceUser(u)
}

func toServiceUser(u *user.User) (*serviceUser, error) {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("parse uid %q: %w", u.Uid, err)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("parse gid %q: %w", u.Gid, err)
	}
	group := u.Gid
	if g, err := user.LookupGroupId(u.Gid); err == nil {
		group = g.Name
	}
	return &serviceUser{Name: u.Username, Home: u.HomeDir, Group: group, UID: uid, GID: gid}, nil
}

// daemonServiceResult reports what the install attempt did.
type daemonServiceResult struct {
	Name        string // unit name
	UnitPath    string
	Mode        string // "system" | "user"
	ServiceUser string // system mode: the account the daemon runs as
	OK          bool
	Err         error
}

// renderSystemUnit builds a system-level unit that runs the daemon as a
// non-root User=. exePath/stateDir are systemd-quoted.
func renderSystemUnit(exePath, stateDir string, u *serviceUser) (string, error) {
	if strings.ContainsAny(exePath, "\r\n") || strings.ContainsAny(stateDir, "\r\n") {
		return "", errors.New("paths cannot contain newlines")
	}
	return fmt.Sprintf(`[Unit]
Description=agbridge daemon
Documentation=https://github.com/pw0rld/agbridge
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
ExecStart=%s daemon --state-dir %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, u.Name, u.Group, systemdQuoteArg(exePath), systemdQuoteArg(stateDir)), nil
}

// renderUserUnit builds a --user unit (no User= line; runs as the
// invoking user).
func renderUserUnit(exePath, stateDir string) (string, error) {
	if strings.ContainsAny(exePath, "\r\n") || strings.ContainsAny(stateDir, "\r\n") {
		return "", errors.New("paths cannot contain newlines")
	}
	return fmt.Sprintf(`[Unit]
Description=agbridge daemon
Documentation=https://github.com/pw0rld/agbridge
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon --state-dir %s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, systemdQuoteArg(exePath), systemdQuoteArg(stateDir)), nil
}

func systemdQuoteArg(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, `%`, `%%`)
	return `"` + s + `"`
}

// setupDaemonService installs and starts a systemd unit for the daemon.
// Running as root installs a system service that runs as a detected
// non-root user; otherwise it installs a --user service. It is
// best-effort: on any failure it returns a result whose Err is set so
// the caller can fall back to printing manual instructions.
//
// stateDir is the path already written by enroll; serviceUser is the
// optional --service-user override (system mode only).
func setupDaemonService(ctx context.Context, stateDir, serviceUser string) daemonServiceResult {
	res := daemonServiceResult{Name: daemonSystemdServiceName}
	if runtime.GOOS != "linux" {
		res.Err = errors.New("systemd service install is only supported on Linux")
		return res
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		res.Err = errors.New("systemctl not found")
		return res
	}
	exe, err := os.Executable()
	if err != nil {
		res.Err = fmt.Errorf("resolve executable: %w", err)
		return res
	}
	if exe, err = filepath.Abs(exe); err != nil {
		res.Err = fmt.Errorf("resolve executable: %w", err)
		return res
	}
	if stateDir, err = filepath.Abs(stateDir); err != nil {
		res.Err = fmt.Errorf("resolve state dir: %w", err)
		return res
	}

	if os.Geteuid() == 0 {
		return setupSystemService(ctx, res, exe, stateDir, serviceUser)
	}
	return setupUserService(ctx, res, exe, stateDir)
}

func setupSystemService(ctx context.Context, res daemonServiceResult, exe, stateDir, serviceUserFlag string) daemonServiceResult {
	res.Mode = "system"
	u, err := resolveServiceUser(serviceUserFlag, osUserLookup{})
	if err != nil {
		res.Err = err
		return res
	}
	res.ServiceUser = u.Name
	unit, err := renderSystemUnit(exe, stateDir, u)
	if err != nil {
		res.Err = err
		return res
	}
	// The daemon runs as u; it must own its state dir + keys.
	if err := chownRecursive(stateDir, u.UID, u.GID); err != nil {
		res.Err = fmt.Errorf("chown %s to %s: %w", stateDir, u.Name, err)
		return res
	}
	res.UnitPath = filepath.Join("/etc/systemd/system", daemonSystemdServiceName)
	if err := os.WriteFile(res.UnitPath, []byte(unit), 0o644); err != nil {
		res.Err = fmt.Errorf("write %s: %w", res.UnitPath, err)
		return res
	}
	if out, err := runSystemctl(ctx, "daemon-reload"); err != nil {
		res.Err = fmt.Errorf("systemctl daemon-reload: %w%s", err, commandOutputSuffix(out))
		return res
	}
	if out, err := runSystemctl(ctx, "enable", "--now", daemonSystemdServiceName); err != nil {
		res.Err = fmt.Errorf("systemctl enable --now: %w%s", err, commandOutputSuffix(out))
		return res
	}
	res.OK = true
	return res
}

func setupUserService(ctx context.Context, res daemonServiceResult, exe, stateDir string) daemonServiceResult {
	res.Mode = "user"
	unit, err := renderUserUnit(exe, stateDir)
	if err != nil {
		res.Err = err
		return res
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		res.Err = fmt.Errorf("resolve home directory: %w", err)
		return res
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		res.Err = fmt.Errorf("create %s: %w", unitDir, err)
		return res
	}
	res.UnitPath = filepath.Join(unitDir, daemonSystemdServiceName)
	if err := os.WriteFile(res.UnitPath, []byte(unit), 0o644); err != nil {
		res.Err = fmt.Errorf("write %s: %w", res.UnitPath, err)
		return res
	}
	if out, err := runUserSystemctl(ctx, "daemon-reload"); err != nil {
		res.Err = fmt.Errorf("systemctl --user daemon-reload: %w%s", err, commandOutputSuffix(out))
		return res
	}
	if out, err := runUserSystemctl(ctx, "enable", "--now", daemonSystemdServiceName); err != nil {
		res.Err = fmt.Errorf("systemctl --user enable --now: %w%s", err, commandOutputSuffix(out))
		return res
	}
	res.OK = true
	return res
}

func chownRecursive(root string, uid, gid int) error {
	return filepath.Walk(root, func(path string, _ os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(path, uid, gid)
	})
}

func runSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
}

func runUserSystemctl(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
}

func commandOutputSuffix(out []byte) string {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		return ""
	}
	return ": " + msg
}

// printDaemonServiceResult writes a human summary + next steps.
func printDaemonServiceResult(w io.Writer, stateDir string, res daemonServiceResult, attempted bool) {
	if attempted && res.OK && res.Mode == "system" {
		fmt.Fprintf(w, "✓ Installed system service %s (runs as user %q)\n", res.Name, res.ServiceUser)
		fmt.Fprintf(w, "✓ Unit: %s\n\n", res.UnitPath)
		fmt.Fprintf(w, "Useful commands:\n")
		fmt.Fprintf(w, "  systemctl status %s\n", res.Name)
		fmt.Fprintf(w, "  journalctl -u %s -f\n", res.Name)
		fmt.Fprintf(w, "\nRuns on boot and survives logout. Change the user with --service-user.\n")
		return
	}
	if attempted && res.OK && res.Mode == "user" {
		fmt.Fprintf(w, "✓ Installed user service %s\n", res.Name)
		fmt.Fprintf(w, "✓ Unit: %s\n\n", res.UnitPath)
		fmt.Fprintf(w, "Keep it running after logout/reboot:\n")
		fmt.Fprintf(w, "  sudo loginctl enable-linger $USER\n\n")
		fmt.Fprintf(w, "Useful commands:\n")
		fmt.Fprintf(w, "  systemctl --user status %s\n", res.Name)
		fmt.Fprintf(w, "  journalctl --user -u %s -f\n", res.Name)
		return
	}
	if attempted && res.Err != nil {
		fmt.Fprintf(w, "⚠ Could not install the systemd service: %v\n\n", res.Err)
	}
	fmt.Fprintf(w, "Start the daemon manually:\n")
	fmt.Fprintf(w, "  agbridge daemon --state-dir %s\n", shellQuoteArg(stateDir))
}

func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
