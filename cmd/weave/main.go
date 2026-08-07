package main

import (
	"context"
	"fmt"
	"os"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/command"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/nativeindex"
	cli "github.com/urfave/cli/v3"
)

func main() {
	database := os.Getenv("WEAVE_DATABASE")
	registrations, registryErr := adapter.LoadRegistry(os.Getenv(adapter.RegistryEnvironment))
	local := application.Local{DatabasePath: database, Adapters: registrations, AdapterConfigError: registryErr}
	if database == "" {
		managerFor := func(directory string) *freshness.Manager {
			provider := nativeindex.Default(directory, registrations...)
			if registryErr != nil {
				provider = nativeindex.ConfigurationError(registryErr)
			}
			return &freshness.Manager{Directory: directory, Provider: provider, Command: "weave"}
		}
		local = application.Local{
			Freshness: managerFor("."), FreshnessFor: managerFor,
			Adapters: registrations, AdapterConfigError: registryErr,
		}
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
