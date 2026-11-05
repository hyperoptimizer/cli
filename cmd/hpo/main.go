package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	telemetry "github.com/hyperoptimizer/cli/internal/telemetry"
	"github.com/hyperoptimizer/cli/internal/cli"
	"github.com/pkg/browser"
)

func main() { os.Exit(run()) }

func run() int {
	closeMonitoring := telemetry.Init("cli")
	defer closeMonitoring()
	defer telemetry.Recover()
	browser.Stdout = os.Stderr
	browser.Stderr = os.Stderr
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	app := cli.New(os.Stdin, os.Stdout, os.Stderr)
	err := app.Command().ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	code := 2
	var failure *cli.ExitError
	if errors.As(err, &failure) {
		code = failure.Code
	}
	if errors.Is(err, context.Canceled) {
		code = 130
	}
	if !errors.As(err, &failure) && !errors.Is(err, context.Canceled) {
		telemetry.Report(err, "command")
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	return code
}
