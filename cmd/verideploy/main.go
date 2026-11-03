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
	app := &cli.App{
		In:            os.Stdin,
		Out:           os.Stdout,
		Err:           os.Stderr,
		Host:          host.New(),
		Download:      network.Client(true),
		HTTP:          network.Client(false),
		Euid:          os.Geteuid,
		UserConfigDir: os.UserConfigDir,
		PollInterval:  2 * time.Second,
	}
	return app.Run(ctx, args)
}
