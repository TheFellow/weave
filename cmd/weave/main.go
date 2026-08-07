package main

import (
	"context"
	"fmt"
	"os"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/command"
	"github.com/TheFellow/weave/internal/freshness"
	cli "github.com/urfave/cli/v3"
)

func main() {
	database := os.Getenv("WEAVE_DATABASE")
	local := application.Local{DatabasePath: database}
	if database == "" {
		manager := &freshness.Manager{Directory: ".", Provider: freshness.EmptyProvider{}, Command: "weave"}
		local = application.Local{Freshness: manager}
	}
	app := command.New(local, command.Streams{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exitCoder, ok := err.(cli.ExitCoder); ok {
			os.Exit(exitCoder.ExitCode())
		}
		os.Exit(1)
	}
}
