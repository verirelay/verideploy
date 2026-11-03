package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/verirelay/protocol/guest"

	"github.com/verirelay/deploy/internal/bootstrap"
	"github.com/verirelay/deploy/internal/config"
	"github.com/verirelay/deploy/internal/release"
)

// answers are the values verideploy init writes.
type answers struct {
	Instance, Release, VeriProv, Profile, Memory, Forward string
	VCPUs                                                 int
	// Latest is the latest release tag, empty when it could not be looked up.
	Latest string
}

// init writes a new config file: from answers read on In, or with --template
// from the defaults for the user to edit.
func (a *App) init(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(a.Err)
	fs.Usage = func() {}
	path := fs.String("c", "deploy.toml", "config file to write")
	template := fs.Bool("template", false, "write the defaults without asking")
	if err := fs.Parse(args); err != nil {
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
	abs, err := filepath.Abs(*path)
	if err != nil {
		return a.fail(err)
	}
	if _, err := os.Lstat(abs); err == nil {
		return a.fail(fmt.Errorf("%s already exists; edit it instead", abs))
	}
	dir := filepath.Dir(abs)
	ans, lookupErr := a.defaults(ctx, dir)
	if !*template {
		if lookupErr != nil {
			a.printf("The latest release could not be looked up: %v\n\n", lookupErr)
		}
		if ans, err = a.ask(ctx, ans); err != nil {
			return a.fail(err)
		}
		if _, err := config.Parse(render(ans), dir); err != nil {
			return a.fail(err)
		}
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G302: the config is public; plan and status read it without root.
	if err != nil {
		return a.fail(err)
	}
	_, err = f.Write(render(ans))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return a.fail(err)
	}
	switch {
	case !*template:
		a.printf("\nWrote %s.\nNext: sudo verideploy fetch, then verideploy plan.\n", abs)
	case ans.Release == "":
		a.printf("Wrote %s. Fill in veriprov and release: the latest release could not be looked up (%v).\n", abs, lookupErr)
	default:
		a.printf("Wrote %s with release %s, the latest.\nFill in veriprov, then run sudo verideploy fetch and verideploy plan.\n", abs, ans.Release)
	}
	return exitOK
}

// defaults returns the default answers for an instance in dir, and why the
// latest release is missing when it could not be looked up.
func (a *App) defaults(ctx context.Context, dir string) (answers, error) {
	ans := answers{Instance: "relay", VCPUs: 8, Memory: "16G", Forward: freeForward(ctx)}
	if name := filepath.Base(dir); config.CheckInstance(name) == nil {
		ans.Instance = name
	}
	releases := a.Releases
	if releases == "" {
		releases = config.DefaultReleaseRepository
	}
	tag, err := release.Latest(ctx, a.HTTP, releases)
	if err == nil {
		err = config.CheckRelease(tag)
	}
	if err != nil {
		return ans, err
	}
	ans.Release, ans.Latest = tag, tag
	return ans, nil
}

// freeForward returns the first free loopback port from 18081, so instances
// on one host get distinct ports by default.
func freeForward(ctx context.Context) string {
	for port := 18081; port < 18181; port++ {
		addr := "127.0.0.1:" + strconv.Itoa(port)
		if l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr); err == nil {
			_ = l.Close()
			return addr
		}
	}
	return "127.0.0.1:18081"
}

// reader asks for one value, showing value as the default, until check
// accepts the answer.
type reader func(label, value string, check func(string) error) (string, error)

// ask reads each answer from In, keeping the default on an empty line and
// asking again until the answer is valid.
func (a *App) ask(ctx context.Context, ans answers) (answers, error) {
	in := bufio.NewScanner(a.In)
	var read reader = func(label, value string, check func(string) error) (string, error) {
		hint := value
		if value != "" && value == ans.Latest {
			hint += ", the latest"
		}
		for {
			if hint != "" {
				a.printf("%s [%s]: ", label, hint)
			} else {
				a.printf("%s: ", label)
			}
			if !in.Scan() {
				if err := in.Err(); err != nil {
					return "", err
				}
				a.printf("\n")
				return "", errors.New("input ended before the config was complete; nothing written")
			}
			answer := strings.TrimSpace(in.Text())
			if answer == "" {
				answer = value
			}
			err := check(answer)
			if err == nil {
				return answer, nil
			}
			a.printf("  %v\n", err)
		}
	}
	var err error
	if ans.Instance, err = read("Instance name", ans.Instance, config.CheckInstance); err != nil {
		return ans, err
	}
	if ans.Release, err = read("Release", ans.Release, config.CheckRelease); err != nil {
		return ans, err
	}
	if ans.VeriProv, ans.Profile, err = a.askVeriProv(ctx, read); err != nil {
		return ans, err
	}
	vcpus, err := read("vCPU count", strconv.Itoa(ans.VCPUs), func(s string) error {
		if n, err := strconv.Atoi(s); err != nil || n < 1 {
			return errors.New("vCPU count must be a positive whole number")
		}
		return nil
	})
	if err != nil {
		return ans, err
	}
	ans.VCPUs, _ = strconv.Atoi(vcpus)
	if ans.Memory, err = read("Guest memory", ans.Memory, func(s string) error {
		_, err := config.ParseMemory(s)
		return err
	}); err != nil {
		return ans, err
	}
	if ans.Forward, err = read("Host address forwarded to VeriRelay", ans.Forward, func(s string) error {
		_, err := config.ParseForward(s)
		return err
	}); err != nil {
		return ans, err
	}
	return ans, nil
}

// askVeriProv asks for the VeriProv and, when it serves several Bootstraps,
// for the profile, then shows the HOST_DATA of the chosen Bootstrap. A
// VeriProv that cannot be reached is kept with a warning: fetch tries again.
func (a *App) askVeriProv(ctx context.Context, read reader) (origin, profile string, err error) {
	var entries []bootstrap.Entry
	origin, err = read("VeriProv address", "", func(s string) error {
		if err := config.CheckVeriProv(s); err != nil {
			return err
		}
		var listErr error
		if entries, listErr = bootstrap.List(ctx, a.Host.HTTP, s); listErr != nil {
			a.printf("  %v\n  verideploy fetch will try again.\n", listErr)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	switch len(entries) {
	case 0:
		profile, err = read("Bootstrap profile", "", config.CheckProfile)
		return origin, profile, err
	case 1:
		profile = entries[0].Profile
	default:
		profile, err = read("Bootstrap profile, one of "+bootstrap.Profiles(entries), entries[0].Profile, func(s string) error {
			if !slices.ContainsFunc(entries, func(e bootstrap.Entry) bool { return e.Profile == s }) {
				return fmt.Errorf("VeriProv has no Bootstrap with profile %q", s)
			}
			return nil
		})
		if err != nil {
			return "", "", err
		}
	}
	data, err := bootstrap.Select(entries, origin, profile)
	if err != nil {
		return "", "", err
	}
	a.printf("  Bootstrap with profile %q, HOST_DATA %x\n", profile, guest.HostData(data))
	return origin, profile, nil
}

// render writes ans as a commented config file.
func render(ans answers) []byte {
	releaseNote := "Fill in a release tag of verirelay/release; the latest could not be looked up."
	switch {
	case ans.Release != "" && ans.Release == ans.Latest:
		releaseNote = "Release tag of verirelay/release; the latest when this file was written."
	case ans.Release != "":
		releaseNote = "Release tag of verirelay/release."
	}
	return fmt.Appendf(nil, `# Deploy instance config: https://github.com/verirelay/deploy/blob/main/docs/cli.md#config-file
version = %q

# Instance name; its unit is verirelay-<instance>.service.
instance = %s

# %s
release = %s

# HTTPS address of your VeriProv. fetch downloads this instance's Bootstrap from it.
veriprov = %s

# Profile of the Bootstrap, when your VeriProv serves more than one.
profile = %s

# Optional keys:
# qemu = %q
# release_repository = %q
# release_keys = [%q]
# registry = %q

[machine]
# vCPU count; the release lists a Launch Measurement for each count it supports.
vcpus = %d
# Guest memory, such as 16G or 4096M.
memory = %s

[network]
# Host IPv4 address and port forwarded to VeriRelay; point your proxy here.
forward = %s
`, config.Version, quoteTOML(ans.Instance), releaseNote, quoteTOML(ans.Release), quoteTOML(ans.VeriProv), quoteTOML(ans.Profile),
		config.DefaultQEMU, config.DefaultReleaseRepository, config.OfficialReleaseKey, config.DefaultRegistry,
		ans.VCPUs, quoteTOML(ans.Memory), quoteTOML(ans.Forward))
}

// quoteTOML returns s as a TOML basic string; JSON string escapes are valid
// TOML escapes.
func quoteTOML(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
