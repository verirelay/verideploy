// Package cli implements the verideploy commands.
package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/verirelay/verideploy/internal/atomic"
	"github.com/verirelay/verideploy/internal/bootstrap"
	"github.com/verirelay/verideploy/internal/config"
	"github.com/verirelay/verideploy/internal/host"
	"github.com/verirelay/verideploy/internal/plan"
	"github.com/verirelay/verideploy/internal/probe"
	"github.com/verirelay/verideploy/internal/release"
)

const Help = `VeriDeploy launches and upgrades VeriRelay CVMs on an AMD SEV-SNP host.

Usage:
  verideploy init   [-c FILE] [options]   Write a new config, asking for each value
  verideploy fetch  [-c FILE]             Download and verify the release and the Bootstrap
  verideploy plan   [-c FILE]             Check the host and show what up would launch
  verideploy up     [-c FILE] [options]   Install the unit, start the CVM, wait until ready
  verideploy down   [-c FILE]             Stop and disable the CVM
  verideploy status [-c FILE]             Compare what the CVM attests with what up launched
  verideploy version                      Print the version
  verideploy -h                           Show this help

Options:
  -c FILE          Instance config (default deploy.toml)
  --template       init: write the defaults, with the latest release, without asking
  --restart        up: restart the CVM even when it runs the current plan
  --timeout DUR    up: how long to wait for /readyz (default 3m)

Exits: 0 success, 1 a check failed, 2 usage error.
`

const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
)

// App holds what the commands touch. Tests replace every field.
type App struct {
	// In carries the answers to verideploy init.
	In       io.Reader
	Out, Err io.Writer
	Host     host.Host
	// Download follows redirects, as GitHub release downloads need.
	Download *http.Client
	// HTTP follows no redirects.
	HTTP *http.Client
	// Euid and UserConfigDir choose the systemd manager: root uses the
	// system's, anyone else their own under UserConfigDir()/systemd/user.
	Euid          func() int
	UserConfigDir func() (string, error)
	// PollInterval is how often up polls /readyz.
	PollInterval time.Duration
	// Releases is where init looks up the latest release; empty means
	// config.DefaultReleaseRepository.
	Releases string
}

// Run executes one command and returns its exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		_, _ = io.WriteString(a.Err, Help)
		return exitUsage
	}
	if args[0] == "init" {
		return a.init(ctx, args[1:])
	}
	commands := map[string]func(context.Context, config.Config, options) int{
		"fetch": a.fetch, "plan": a.plan, "up": a.up, "down": a.down, "status": a.status,
	}
	command, ok := commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(a.Err, "Unknown command %q. Use verideploy -h.\n", args[0])
		return exitUsage
	}
	var o options
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() {}
	fs.StringVar(&o.config, "c", "deploy.toml", "instance config")
	if args[0] == "up" {
		fs.BoolVar(&o.restart, "restart", false, "restart even when unchanged")
		fs.DurationVar(&o.timeout, "timeout", 3*time.Minute, "readyz wait")
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = io.WriteString(a.Out, Help)
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(a.Err, "Unexpected argument %q. Use verideploy -h.\n", fs.Arg(0))
		return exitUsage
	}
	c, err := config.Load(o.config)
	if err != nil {
		_, _ = fmt.Fprintln(a.Err, err)
		return exitUsage
	}
	return command(ctx, c, o)
}

type options struct {
	config  string
	restart bool
	timeout time.Duration
}

func (a *App) fail(err error) int {
	_, _ = fmt.Fprintln(a.Err, err)
	return exitFailed
}

func (a *App) printf(format string, args ...any) { _, _ = fmt.Fprintf(a.Out, format, args...) }

func releaseDir(c config.Config) string { return filepath.Join(c.Dir, "releases", c.Release) }

func open(c config.Config, dir string) (release.Release, error) {
	return release.Open(dir, c.Release, c.VCPUs, c.ReleaseRepository, c.ReleaseKeys)
}

func (a *App) fetch(ctx context.Context, c config.Config, _ options) int {
	a.printf("Fetching %s from %s\n", c.Release, c.ReleaseRepository)
	verify := func(dir string) error {
		_, err := open(c, dir)
		return err
	}
	if err := release.Fetch(ctx, a.Download, c.ReleaseRepository, c.Release, releaseDir(c), verify, a.Out); err != nil {
		return a.fail(err)
	}
	r, err := open(c, releaseDir(c))
	if err != nil {
		return a.fail(err)
	}
	a.printf("Verified %s: Build Record for %d vCPU signed by %s\n", r.Tag, r.VCPUs, r.KeyID)
	if !a.registry(ctx, c, r) {
		return exitFailed
	}
	data, err := bootstrap.Fetch(ctx, a.Host.HTTP, c.VeriProv, c.Profile)
	if err != nil {
		return a.fail(err)
	}
	hostData := sha256.Sum256(data)
	current, readErr := os.ReadFile(c.Bootstrap)
	if readErr == nil && bytes.Equal(current, data) {
		a.printf("Bootstrap from %s unchanged: HOST_DATA %x\n", c.VeriProv, hostData)
		return exitOK
	}
	if err := atomic.WriteFile(c.Bootstrap, data, 0o644); err != nil {
		return a.fail(err)
	}
	if readErr == nil {
		old := sha256.Sum256(current)
		a.printf("Bootstrap from %s changed: HOST_DATA %x, was %x\n", c.VeriProv, hostData, old)
	} else {
		a.printf("Bootstrap from %s saved: HOST_DATA %x\n", c.VeriProv, hostData)
	}
	return exitOK
}

// registry runs the Registry check and reports whether it allows r.
func (a *App) registry(ctx context.Context, c config.Config, r release.Release) bool {
	if c.Registry == "" {
		return true
	}
	failure, err := release.CheckRegistry(ctx, a.HTTP, c.Registry, r.Measurement, c.ReleaseKeys)
	switch {
	case failure != "":
		_, _ = fmt.Fprintf(a.Err, "%s\nThe Verifier fails the code verdict of this release.\n", failure)
		return false
	case err != nil:
		a.printf("Warning: Registry check undecided: %v\n", err)
	default:
		a.printf("Not revoked in %s\n", c.Registry)
	}
	return true
}

// resolved is a derived Plan with what it was derived from.
type resolved struct {
	plan      plan.Plan
	bootstrap []byte
	// launched is the Plan up last launched, zero when there is none.
	launched plan.Plan
}

// resolve verifies the release, derives the Plan and runs the checks. It
// prints both and reports whether every check passed.
func (a *App) resolve(ctx context.Context, c config.Config) (resolved, bool) {
	var out resolved
	r, err := open(c, releaseDir(c))
	if err != nil {
		_, _ = fmt.Fprintf(a.Err, "%v\nRun verideploy fetch first, or place the release in %s.\n", err, releaseDir(c))
		return out, false
	}
	if out.bootstrap, err = os.ReadFile(c.Bootstrap); err != nil {
		_, _ = fmt.Fprintf(a.Err, "%v\nRun verideploy fetch first: it downloads the Bootstrap from %s.\n", err, c.VeriProv)
		return out, false
	}
	s, err := a.scope()
	if err != nil {
		_, _ = fmt.Fprintln(a.Err, err)
		return out, false
	}
	p, err := plan.Resolve(c, r, out.bootstrap, s)
	if err != nil {
		_, _ = fmt.Fprintln(a.Err, err)
		return out, false
	}
	out.plan = p
	if out.launched, err = readPlan(c.Dir); err != nil {
		_, _ = fmt.Fprintln(a.Err, err)
		return out, false
	}
	a.printf("Release      %s (Build Record signed by %s)\n", p.Release, r.KeyID)
	a.printf("vCPU         %d\nMemory       %s\n", p.VCPUs, p.Memory)
	a.printf("Measurement  %s\nHOST_DATA    %s\n", p.Measurement, p.HostData)
	a.printf("VeriProv     %s\nForward      %s -> guest %d\n", p.VeriProv, strings.Join(p.Forward, ", "), plan.GuestPort)
	ok := a.registry(ctx, c, r)
	a.printf("\nPreflight\n")
	for _, check := range a.Host.Preflight(ctx, c, p, out.launched) {
		if check.Err != nil {
			ok = false
			a.printf("  FAIL  %-9s %v\n", check.Name, check.Err)
		} else {
			a.printf("  ok    %-9s %s\n", check.Name, check.Detail)
		}
	}
	installed, err := a.Host.InstalledUnit(p)
	switch {
	case err != nil:
		ok = false
		a.printf("\nUnit %s: %v\n", p.UnitPath, err)
	case installed == "":
		a.printf("\nUnit %s: not installed. up writes:\n\n", p.UnitPath)
		for line := range strings.Lines(p.Unit) {
			if line != "\n" {
				line = "  " + line
			}
			a.printf("%s", line)
		}
	case !p.Owns(installed):
		ok = false
		a.printf("\nUnit %s belongs to another config file, or was not written by VeriDeploy:\n  %s\nRename this instance, or remove that unit first.\n", p.UnitPath, firstLine(installed))
	case installed == p.Unit:
		a.printf("\nUnit %s: unchanged\n", p.UnitPath)
	default:
		a.printf("\nUnit %s: changes\n", p.UnitPath)
		for _, line := range diff(installed, p.Unit) {
			a.printf("  %s\n", line)
		}
	}
	a.printf("\nVeriProv must accept measurement %s and HOST_DATA %s.\n", p.Measurement, p.HostData)
	return out, ok
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// readPlan returns the Plan up last wrote to dir, zero when there is none.
func readPlan(dir string) (plan.Plan, error) {
	data, err := os.ReadFile(plan.Path(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return plan.Plan{}, nil
	}
	if err != nil {
		return plan.Plan{}, err
	}
	var p plan.Plan
	if err := json.Unmarshal(data, &p); err != nil || p.Version != plan.Version || len(p.Forward) == 0 {
		return plan.Plan{}, fmt.Errorf("%s is not a %s file", plan.Path(dir), plan.Version)
	}
	return p, nil
}

func (a *App) plan(ctx context.Context, c config.Config, _ options) int {
	if _, ok := a.resolve(ctx, c); !ok {
		return exitFailed
	}
	return exitOK
}

// scope picks the systemd manager for plan and up: the system manager for
// root, the user's own otherwise, so QEMU runs as whoever runs up.
func (a *App) scope() (plan.Scope, error) {
	if a.Euid() == 0 {
		return plan.Scope{Dir: plan.SystemUnitDir}, nil
	}
	dir, err := a.UserConfigDir()
	if err != nil {
		return plan.Scope{}, err
	}
	return plan.Scope{User: true, Dir: filepath.Join(dir, "systemd", "user")}, nil
}

func (a *App) up(ctx context.Context, c config.Config, o options) int {
	r, ok := a.resolve(ctx, c)
	if !ok {
		_, _ = fmt.Fprintln(a.Err, "\nNothing changed. Fix the failures above first.")
		return exitFailed
	}
	p := r.plan
	disk := plan.ConfigDisk(r.bootstrap)
	if current, err := os.ReadFile(p.ConfigDisk); err != nil || !bytes.Equal(current, disk) {
		if err := atomic.WriteFile(p.ConfigDisk, disk, 0o644); err != nil {
			return a.fail(err)
		}
	}
	if _, err := a.Host.InstallUnit(p); err != nil {
		return a.fail(err)
	}
	unit := plan.UnitName(p.Instance)
	for _, step := range [][]string{{"daemon-reload"}, {"enable", unit}} {
		if err := a.Host.Systemctl(ctx, p.UserUnit, step...); err != nil {
			return a.fail(err)
		}
	}
	// Decide from what was last launched, not from the unit file: an earlier
	// up may have written the unit and failed before restarting.
	restart := o.restart || r.launched.Unit != p.Unit || !a.Host.Active(ctx, p.UserUnit, unit)
	since := time.Now().Add(-time.Second)
	if restart {
		if err := a.Host.Systemctl(ctx, p.UserUnit, "restart", unit); err != nil {
			return a.fail(err)
		}
		record, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return a.fail(err)
		}
		if err := atomic.WriteFile(plan.Path(c.Dir), append(record, '\n'), 0o644); err != nil {
			return a.fail(err)
		}
		a.printf("\nStarted %s. Waiting up to %s for /readyz.\n", unit, o.timeout)
	} else {
		a.printf("\n%s already runs this plan.\n", unit)
	}
	waitCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	err := probe.WaitReady(waitCtx, a.HTTP, probe.BaseURL(p.Forward[0]), a.PollInterval)
	if err == nil {
		a.printf("Ready. verideploy status compares what it attests with this plan.\n")
		return exitOK
	}
	_, _ = fmt.Fprintf(a.Err, "Not ready: %v\n", err)
	journal, jerr := a.Host.Journal(ctx, p.UserUnit, unit, since)
	if cause := probe.Diagnose(journal, p); jerr == nil && cause != "" {
		_, _ = fmt.Fprintf(a.Err, "Likely cause: %s\n", cause)
	}
	logs := "journalctl -u " + unit
	if p.UserUnit {
		logs = "journalctl --user -u " + unit
	}
	_, _ = fmt.Fprintf(a.Err, "Full log: %s\n", logs)
	return exitFailed
}

// down stops the unit in the manager up used, as plan.json records it.
func (a *App) down(ctx context.Context, c config.Config, _ options) int {
	launched, err := readPlan(c.Dir)
	if err != nil {
		return a.fail(err)
	}
	user := launched.UserUnit
	if launched.Version == "" {
		user = a.Euid() != 0
	}
	unit := plan.UnitName(c.Instance)
	if err := a.Host.Systemctl(ctx, user, "disable", "--now", unit); err != nil {
		return a.fail(err)
	}
	a.printf("Stopped and disabled %s. Its files are kept.\n", unit)
	return exitOK
}

func (a *App) status(ctx context.Context, c config.Config, _ options) int {
	p, err := readPlan(c.Dir)
	if err != nil {
		return a.fail(err)
	}
	if p.Version == "" {
		return a.fail(errors.New("no plan.json: run verideploy up first, since status compares against what up launched"))
	}
	unit := plan.UnitName(p.Instance)
	ok := true
	report := func(pass bool, format string, args ...any) {
		mark := "ok  "
		if !pass {
			mark, ok = "FAIL", false
		}
		a.printf("  %s  "+format+"\n", append([]any{mark}, args...)...)
	}
	a.printf("%s: %s, %d vCPU\n", unit, p.Release, p.VCPUs)
	state := a.Host.State(ctx, p.UserUnit, unit)
	report(state == "active", "unit %s", state)
	installed, err := a.Host.InstalledUnit(p)
	report(err == nil && installed == p.Unit, "installed unit matches plan.json")
	base := probe.BaseURL(p.Forward[0])
	err = probe.Ready(ctx, a.HTTP, base)
	report(err == nil, "%s/readyz %s", base, okOr(err))
	att, err := probe.Attest(ctx, a.HTTP, base)
	if err != nil {
		report(false, "%v", err)
		return exitCode(ok)
	}
	disclosed := sha256.Sum256(att.Bootstrap)
	report(att.Bound, "REPORT_DATA binds this request's nonce")
	report(att.Measurement == p.Measurement, "measurement %s", att.Measurement)
	report(att.HostData == p.HostData, "HOST_DATA %s", att.HostData)
	report(att.Bootstrap != nil && hex.EncodeToString(disclosed[:]) == p.HostData, "disclosed Bootstrap hashes to HOST_DATA")
	return exitCode(ok)
}

func okOr(err error) string {
	if err != nil {
		return err.Error()
	}
	return "200"
}

func exitCode(ok bool) int {
	if ok {
		return exitOK
	}
	return exitFailed
}
