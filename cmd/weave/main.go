package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/TheFellow/weave/internal/adapter"
	"github.com/TheFellow/weave/internal/application"
	"github.com/TheFellow/weave/internal/command"
	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/nativeindex"
	cli "github.com/urfave/cli/v3"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database := os.Getenv("WEAVE_DATABASE")
	store, storeErr := adapter.DefaultStore()
	managed, _, managedErr := store.Registrations(ctx)
	environment, environmentErr := adapter.EnvironmentRegistrations(os.Getenv)
	explicit, registryErr := adapter.LoadRegistry(os.Getenv(adapter.RegistryEnvironment))
	registrations, environmentMergeErr := adapter.MergeRegistrations(managed, environment)
	registrations, explicitMergeErr := adapter.MergeRegistrations(registrations, explicit)
	automaticClaimsErr := adapter.ValidateAutomaticClaims(registrations)
	configurationErr := errors.Join(storeErr, managedErr, environmentErr, registryErr, environmentMergeErr, explicitMergeErr, automaticClaimsErr)
	local := application.Local{DatabasePath: database, Adapters: registrations, AdapterConfigError: configurationErr, AdapterStore: store}
	if database == "" {
		managerFor := func(directory string) *freshness.Manager {
			provider := nativeindex.Default(directory, registrations...)
			if configurationErr != nil {
				provider = nativeindex.ConfigurationError(configurationErr)
			}
			return &freshness.Manager{Directory: directory, Provider: provider, Command: "weave"}
		}
		local = application.Local{
			Freshness: managerFor("."), FreshnessFor: managerFor,
			Adapters: registrations, AdapterConfigError: configurationErr, AdapterStore: store,
		}
	}
	app := command.New(local, command.Streams{
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	})

	if err := app.Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if exitCoder, ok := err.(cli.ExitCoder); ok {
			os.Exit(exitCoder.ExitCode())
		}
		os.Exit(1)
	}
}
