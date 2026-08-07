package main

import (
	"context"
	"fmt"
	"os"

	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/command"
	cli "github.com/urfave/cli/v3"
)

func main() {
	app := command.New(application.Noop{}, command.Streams{
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
