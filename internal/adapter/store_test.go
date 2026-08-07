package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedStoreInstallUpdateRemoveAndTamper(t *testing.T) {
	t.Setenv("WEAVE_MANAGED_HELPER", "1")
	store := Store{Root: filepath.Join(t.TempDir(), "state")}
	options := InstallOptions{
		Source: os.Args[0], Arguments: []string{"-test.run=TestManagedAdapterHelper", "--"},
		Permissions: Permissions{BuildTool: true}, Timeout: 17 * time.Second,
	}
	installed, err := store.Install(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "fixture-managed" || !validDigest(installed.ArtifactDigest) || !validDigest(installed.CapabilityDigest) {
		t.Fatalf("installation = %#v", installed)
	}
	registrations, values, err := store.Registrations(context.Background())
	if err != nil || len(registrations) != 1 || len(values) != 1 || registrations[0].IntegrityError != "" {
		t.Fatalf("registrations=%#v values=%#v err=%v", registrations, values, err)
	}
	if _, err := store.Install(context.Background(), options); err == nil {
		t.Fatal("duplicate install succeeded")
	}
	updated, err := store.Install(context.Background(), InstallOptions{Source: os.Args[0], UpdateName: "fixture-managed"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(updated.Arguments, options.Arguments) || !updated.Permissions.BuildTool || updated.Timeout != "17s" {
		t.Fatalf("update did not preserve policy: %#v", updated)
	}
	artifact := filepath.Join(store.Root, installed.Artifact)
	backup := artifact + ".real"
	if err := os.Rename(artifact, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backup, artifact); err == nil {
		registrations, _, err = store.Registrations(context.Background())
		if err != nil || registrations[0].IntegrityError != "" {
			t.Fatalf("metadata-only registrations=%#v err=%v", registrations, err)
		}
		if err := VerifyArtifact(artifact, registrations[0].ArtifactDigest); err == nil {
			t.Fatal("installed artifact symlink passed verification")
		}
		if err := os.Remove(artifact); err != nil {
			t.Fatal(err)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	if err := os.Rename(backup, artifact); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(artifact, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("tamper")
	_ = file.Close()
	registrations, _, err = store.Registrations(context.Background())
	if err != nil || registrations[0].IntegrityError != "" {
		t.Fatalf("tamper registrations=%#v err=%v", registrations, err)
	}
	if err := VerifyArtifact(artifact, registrations[0].ArtifactDigest); err == nil {
		t.Fatal("tampered artifact passed verification")
	}
	if err := store.Remove(context.Background(), "fixture-managed"); err != nil {
		t.Fatal(err)
	}
	if values, err := store.Load(context.Background()); err != nil || len(values) != 0 {
		t.Fatalf("after remove=%#v err=%v", values, err)
	}
}

func TestManagedStoreRejectsSymlinkAndSerializesConcurrentInstall(t *testing.T) {
	t.Setenv("WEAVE_MANAGED_HELPER", "1")
	directory := t.TempDir()
	symlink := filepath.Join(directory, "adapter")
	if err := os.Symlink(os.Args[0], symlink); err == nil {
		if _, err := (Store{Root: filepath.Join(directory, "symlink-state")}).Install(context.Background(), InstallOptions{Source: symlink}); err == nil {
			t.Fatal("symlink install succeeded")
		}
	}
	store := Store{Root: filepath.Join(directory, "state")}
	options := InstallOptions{Source: os.Args[0], Arguments: []string{"-test.run=TestManagedAdapterHelper", "--"}}
	var wait sync.WaitGroup
	errors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() { defer wait.Done(); _, err := store.Install(context.Background(), options); errors <- err }()
	}
	wait.Wait()
	close(errors)
	var successes int
	for err := range errors {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent installs = %d", successes)
	}
}

func TestManagedStoreRecoversInterruptedWindowsStyleReplacement(t *testing.T) {
	store := Store{Root: t.TempDir()}
	document := installationManifest{Schema: InstallationSchema}
	encoded, _ := json.Marshal(document)
	if err := os.WriteFile(store.manifestPath()+".previous", encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := store.Load(context.Background())
	if err != nil || len(values) != 0 {
		t.Fatalf("recovery values=%#v err=%v", values, err)
	}
	if _, err := os.Stat(store.manifestPath()); err != nil {
		t.Fatalf("recovered manifest: %v", err)
	}
	if err := os.WriteFile(store.manifestPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background()); err == nil {
		t.Fatal("corrupt manifest accepted")
	}
}

func TestManagedStoreRejectsManifestPathsOutsideOwnedBin(t *testing.T) {
	store := Store{Root: t.TempDir()}
	capabilities := Capabilities{Protocols: []string{Protocol}, Provider: Provider{Name: "fixture-managed", Version: "1.0.0"}, Languages: []string{"fixture"}, Operations: []string{"index"}, RefreshModes: []string{"full"}, FactEncoding: FactEncoding, PositionEncoding: []string{"utf8-byte"}, Claims: Claims{Inputs: Inputs{Extensions: []string{".fixture"}}, Evidence: []string{"exact"}}}
	capabilityDigest, err := CapabilityDigest(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []string{"manifest.json", filepath.Join("nested", "adapter"), ".."} {
		t.Run(artifact, func(t *testing.T) {
			document := installationManifest{Schema: InstallationSchema, Installations: []Installation{{
				Name: "fixture-managed", Artifact: artifact, ArtifactDigest: "sha256:" + strings.Repeat("0", 64),
				CapabilityDigest: capabilityDigest, Capabilities: capabilities,
			}}}
			encoded, _ := json.Marshal(document)
			if err := os.WriteFile(store.manifestPath(), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(context.Background()); err == nil {
				t.Fatalf("unsafe artifact %q was accepted", artifact)
			}
		})
	}
}

func TestManagedAdapterHelper(t *testing.T) {
	if os.Getenv("WEAVE_MANAGED_HELPER") != "1" {
		return
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	operation := os.Args[separator+1]
	if operation != "describe" {
		os.Exit(2)
	}
	value := Capabilities{Protocols: []string{Protocol}, Provider: Provider{Name: "fixture-managed", Version: "1.0.0"}, Languages: []string{"fixture"}, Operations: []string{"index"}, RefreshModes: []string{"full"}, FactEncoding: FactEncoding, PositionEncoding: []string{"utf8-byte"}, Claims: Claims{Inputs: Inputs{Extensions: []string{".fixture"}}, Evidence: []string{"exact"}}}
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(0)
}

func TestAdapterStateBaseIsCrossPlatform(t *testing.T) {
	home := func() (string, error) { return "/home/r", nil }
	if got, _ := adapterStateBase("linux", home, func(name string) string {
		if name == "XDG_STATE_HOME" {
			return "/state"
		}
		return ""
	}, func() (string, error) { return "/config", nil }); got != "/state" {
		t.Fatalf("linux state = %q", got)
	}
}
