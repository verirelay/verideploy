package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/verirelay/verideploy/internal/cli"
	"github.com/verirelay/verideploy/internal/host"
	"github.com/verirelay/verideploy/internal/network"
)

var version = "dev"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 1 {
		switch args[0] {
		case "-h", "--help", "help":
			fmt.Print(cli.Help)
			return 0
		case "version":
			fmt.Println("VeriDeploy " + version)
			return 0
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	h := host.New()
	// systemctl --user and journalctl --user find the user's manager through
	// XDG_RUNTIME_DIR; fill it in when the shell did not.
	if uid := os.Geteuid(); uid != 0 && os.Getenv("XDG_RUNTIME_DIR") == "" {
		if dir := h.RuntimeDir(uid); dir != "" {
			_ = os.Setenv("XDG_RUNTIME_DIR", dir)
		}
	}
	app := &cli.App{
		In:            os.Stdin,
		Out:           os.Stdout,
		Err:           os.Stderr,
		Host:          h,
		Download:      network.Client(true),
		HTTP:          network.Client(false),
		Euid:          os.Geteuid,
		UserConfigDir: os.UserConfigDir,
		PollInterval:  2 * time.Second,
	}
	return app.Run(ctx, args)
}
