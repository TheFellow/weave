package watch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/freshness"
)

func TestRunInitialReadyAndReadOnlyInitialMode(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		initial    bool
		ensureWant int
		refreshed  bool
	}{
		{name: "initial refresh", initial: true, ensureWant: 1, refreshed: true},
		{name: "read only initial observation", initial: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			clock := newManualClock()
			coordinator := &fakeCoordinator{observation: currentObservation("state-a")}
			coordinator.ensure = func(context.Context, int) (freshness.Status, error) {
				status := coordinator.observation.Status
				status.Refreshed = true
				return status, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			events, done := runAsync(ctx, coordinator, Options{PollInterval: 100 * time.Millisecond, Initial: test.initial}, clock)
			first := receiveEvent(t, events)
			if first.Type != EventReady || first.Sequence != 1 || first.Schema != Schema || first.Observation != "state-a" || first.Status == nil || first.Status.Refreshed != test.refreshed {
				t.Fatalf("ready event = %#v", first)
			}
			cancel()
			if err := receiveDone(t, done); err != nil {
				t.Fatal(err)
			}
			if got := coordinator.ensureCalls(); got != test.ensureWant {
				t.Fatalf("Ensure calls = %d, want %d", got, test.ensureWant)
			}
			if !clock.stopped() {
				t.Fatal("watch ticker was not stopped")
			}
		})
	}
}

func TestRunInitialFailureRecoversToOneReadyEvent(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	// A failed Ensure must be retried even when the cheap source/manifest
	// observation is current (for example, a graph generation verification
	// failure that query-authoritative Ensure detected).
	coordinator := &fakeCoordinator{observation: currentObservation("initial-current")}
	coordinator.ensure = func(_ context.Context, call int) (freshness.Status, error) {
		if call == 1 {
			return freshness.Status{}, errors.New("cold provider failed")
		}
		observation := coordinator.snapshot()
		observation.Status.Current = true
		observation.Status.Refreshed = true
		coordinator.setObservation(observation)
		return observation.Status, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	failure := receiveEvent(t, events)
	if failure.Type != EventError || failure.Trigger != TriggerInitial {
		t.Fatalf("initial failure = %#v", failure)
	}
	clock.advance(50 * time.Millisecond)
	ready := receiveEvent(t, events)
	if ready.Type != EventReady || ready.Trigger != TriggerRetry || ready.Status == nil || !ready.Status.Refreshed || !ready.Status.Current {
		t.Fatalf("recovered ready event = %#v", ready)
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunRetainsFailedInitialEnsureAcrossObservationError(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coordinator := &fakeCoordinator{observation: currentObservation("current-source")}
	coordinator.observe = func(call int) (freshness.Observation, error) {
		if call == 1 {
			return freshness.Observation{}, errors.New("temporary Git observation failure")
		}
		return coordinator.snapshot(), nil
	}
	coordinator.ensure = func(_ context.Context, call int) (freshness.Status, error) {
		if call == 1 {
			return freshness.Status{}, errors.New("generation verification failed")
		}
		status := coordinator.snapshot().Status
		status.Refreshed = true
		return status, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	if event := receiveEvent(t, events); event.Type != EventError || event.Status != nil || event.Observation != "" {
		t.Fatalf("initial failure event = %#v", event)
	}
	clock.advance(50 * time.Millisecond)
	ready := receiveEvent(t, events)
	if ready.Type != EventReady || ready.Trigger != TriggerRetry || ready.Status == nil || !ready.Status.Refreshed || coordinator.ensureCalls() != 2 {
		t.Fatalf("recovered ready event = %#v, Ensure calls %d", ready, coordinator.ensureCalls())
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunNewCurrentObservationBypassesFailedEnsureBackoff(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	stateA := identifiedObservation("state-a-token", "sha256:state-a", true)
	stateB := identifiedObservation("state-b-token", "sha256:state-b", true)
	coordinator := &fakeCoordinator{observation: stateA}
	coordinator.ensure = func(_ context.Context, call int) (freshness.Status, error) {
		if call == 1 {
			return freshness.Status{}, errors.New("generation verification failed")
		}
		status := coordinator.snapshot().Status
		return status, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 100 * time.Millisecond, Initial: true}, clock)
	if failure := receiveEvent(t, events); failure.Type != EventError || failure.Trigger != TriggerInitial {
		t.Fatalf("initial failure = %#v", failure)
	}

	coordinator.setObservation(stateB)
	clock.advance(time.Millisecond)
	ready := receiveEvent(t, events)
	if ready.Type != EventReady || ready.Trigger != TriggerChange || ready.Observation != stateB.Token || coordinator.ensureCalls() != 2 {
		t.Fatalf("new-state recovery = %#v, Ensure calls %d", ready, coordinator.ensureCalls())
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunDoesNotAttachNewStateToCompletedRefresh(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	base := identifiedObservation("base-token", "sha256:base", true)
	stateA := identifiedObservation("state-a-token", "sha256:state-a", false)
	stateB := identifiedObservation("state-b-token", "sha256:state-b", false)
	coordinator := &fakeCoordinator{observation: base}
	coordinator.ensure = func(_ context.Context, call int) (freshness.Status, error) {
		switch call {
		case 1:
			return base.Status, nil
		case 2:
			completed := identifiedObservation("state-a-token", "sha256:state-a", true).Status
			completed.Refreshed = true
			// Simulate an edit after Ensure published A but before its follow-up
			// observation. B has the same coarse dirty/count shape but a different
			// exact generation.
			coordinator.setObservation(stateB)
			return completed, nil
		default:
			currentB := identifiedObservation("state-b-token", "sha256:state-b", true)
			currentB.Status.Refreshed = true
			coordinator.setObservation(currentB)
			return currentB.Status, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	if ready := receiveEvent(t, events); ready.Type != EventReady || ready.Observation != base.Token {
		t.Fatalf("initial ready = %#v", ready)
	}

	coordinator.setObservation(stateA)
	clock.advance(50 * time.Millisecond)
	completedA := receiveEvent(t, events)
	if completedA.Type != EventRefreshed || completedA.Observation != "" || completedA.Status == nil || completedA.Status.Generation != "sha256:state-a" || !completedA.Status.Refreshed {
		t.Fatalf("A completion incorrectly attributed to B = %#v", completedA)
	}

	clock.advance(50 * time.Millisecond)
	completedB := receiveEvent(t, events)
	if completedB.Type != EventRefreshed || completedB.Observation != stateB.Token || completedB.Status == nil || completedB.Status.Generation != "sha256:state-b" || coordinator.ensureCalls() != 3 {
		t.Fatalf("B completion = %#v, Ensure calls %d", completedB, coordinator.ensureCalls())
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunInitialFollowupObservationErrorKeepsReadyCompletion(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	current := identifiedObservation("current-token", "sha256:current", true)
	coordinator := &fakeCoordinator{observation: current}
	coordinator.observe = func(call int) (freshness.Observation, error) {
		if call == 1 {
			return freshness.Observation{}, errors.New("follow-up observation failed")
		}
		return coordinator.snapshot(), nil
	}
	coordinator.ensure = func(context.Context, int) (freshness.Status, error) {
		status := current.Status
		status.Refreshed = true
		return status, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	ready := receiveEvent(t, events)
	if ready.Type != EventReady || ready.Observation != "" || ready.Status == nil || !ready.Status.Refreshed || ready.Status.Generation != current.Status.Generation {
		t.Fatalf("initial completion = %#v", ready)
	}
	if event := receiveEvent(t, events); event.Type != EventError || !strings.Contains(event.Error, "follow-up observation failed") {
		t.Fatalf("follow-up error = %#v", event)
	}
	clock.advance(50 * time.Millisecond)
	assertNoEvent(t, events)
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunLaterFollowupObservationErrorKeepsRefreshCompletion(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	base := identifiedObservation("base-token", "sha256:base", true)
	changed := identifiedObservation("changed-token", "sha256:changed", false)
	coordinator := &fakeCoordinator{observation: base}
	coordinator.observe = func(call int) (freshness.Observation, error) {
		if call == 3 {
			return freshness.Observation{}, errors.New("post-refresh observation failed")
		}
		return coordinator.snapshot(), nil
	}
	coordinator.ensure = func(_ context.Context, call int) (freshness.Status, error) {
		if call == 1 {
			return base.Status, nil
		}
		current := identifiedObservation("changed-token", "sha256:changed", true)
		current.Status.Refreshed = true
		coordinator.setObservation(current)
		return current.Status, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	if ready := receiveEvent(t, events); ready.Type != EventReady {
		t.Fatalf("initial event = %#v", ready)
	}
	coordinator.setObservation(changed)
	clock.advance(50 * time.Millisecond)
	completion := receiveEvent(t, events)
	if completion.Type != EventRefreshed || completion.Observation != "" || completion.Status == nil || !completion.Status.Refreshed || completion.Status.Generation != "sha256:changed" {
		t.Fatalf("refresh completion = %#v", completion)
	}
	if event := receiveEvent(t, events); event.Type != EventError || !strings.Contains(event.Error, "post-refresh observation failed") {
		t.Fatalf("post-refresh error = %#v", event)
	}
	clock.advance(50 * time.Millisecond)
	assertNoEvent(t, events)
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunBacksOffSameObservationAndNewObservationBypassesBackoff(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coordinator := &fakeCoordinator{observation: currentObservation("initial")}
	coordinator.ensure = func(_ context.Context, call int) (freshness.Status, error) {
		switch call {
		case 1:
			return coordinator.snapshot().Status, nil
		case 2:
			return freshness.Status{}, errors.New("provider unavailable")
		default:
			observation := coordinator.snapshot()
			observation.Status.Current = true
			observation.Status.Refreshed = true
			coordinator.setObservation(observation)
			return observation.Status, nil
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 100 * time.Millisecond, Initial: true}, clock)
	_ = receiveEvent(t, events)

	coordinator.setObservation(staleObservation("edit-a"))
	clock.advance(100 * time.Millisecond)
	failure := receiveEvent(t, events)
	if failure.Type != EventError || failure.Trigger != TriggerChange || !strings.Contains(failure.Error, "provider unavailable") {
		t.Fatalf("failure event = %#v", failure)
	}
	if got := coordinator.ensureCalls(); got != 2 {
		t.Fatalf("Ensure calls after failure = %d", got)
	}

	clock.advance(50 * time.Millisecond)
	assertNoEvent(t, events)
	if got := coordinator.ensureCalls(); got != 2 {
		t.Fatalf("same observation bypassed backoff: %d calls", got)
	}

	coordinator.setObservation(staleObservation("edit-b"))
	clock.advance(time.Millisecond)
	refreshed := receiveEvent(t, events)
	if refreshed.Type != EventRefreshed || refreshed.Trigger != TriggerChange || refreshed.Observation != "edit-b" || refreshed.Status == nil || !refreshed.Status.Current {
		t.Fatalf("refresh event = %#v", refreshed)
	}
	if got := coordinator.ensureCalls(); got != 3 {
		t.Fatalf("new observation Ensure calls = %d", got)
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunRetriesUnchangedFailureWithExponentialDelay(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coordinator := &fakeCoordinator{observation: currentObservation("initial")}
	coordinator.ensure = func(_ context.Context, call int) (freshness.Status, error) {
		if call == 1 {
			return coordinator.snapshot().Status, nil
		}
		return freshness.Status{}, errors.New("still broken")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 100 * time.Millisecond, Initial: true}, clock)
	_ = receiveEvent(t, events)
	coordinator.setObservation(staleObservation("same-edit"))

	clock.advance(100 * time.Millisecond)
	first := receiveEvent(t, events)
	if first.Trigger != TriggerChange {
		t.Fatalf("first trigger = %q", first.Trigger)
	}
	clock.advance(99 * time.Millisecond)
	assertNoEvent(t, events)
	clock.advance(time.Millisecond)
	second := receiveEvent(t, events)
	if second.Trigger != TriggerRetry {
		t.Fatalf("retry trigger = %q", second.Trigger)
	}
	clock.advance(199 * time.Millisecond)
	assertNoEvent(t, events)
	if got := coordinator.ensureCalls(); got != 3 {
		t.Fatalf("Ensure calls = %d, want initial plus two failures", got)
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRetryBackoffCapsAtMaximum(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	var retry retryState
	for range 32 {
		retry.failed("same-state", now, 100*time.Millisecond, true)
	}
	if got := retry.nextAttempt.Sub(now); got != maximumRetryBackoff {
		t.Fatalf("retry delay = %s, want %s", got, maximumRetryBackoff)
	}
	if !retry.waiting("same-state", now.Add(maximumRetryBackoff-time.Nanosecond)) || retry.waiting("same-state", now.Add(maximumRetryBackoff)) {
		t.Fatal("capped retry boundary is incorrect")
	}
}

func TestRunCoalescesPollsAndNeverRefreshesConcurrently(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coordinator := &fakeCoordinator{observation: currentObservation("initial")}
	started := make(chan struct{})
	release := make(chan struct{})
	coordinator.ensure = func(ctx context.Context, call int) (freshness.Status, error) {
		if call == 1 {
			return coordinator.snapshot().Status, nil
		}
		close(started)
		select {
		case <-release:
			observation := coordinator.snapshot()
			observation.Status.Current = true
			observation.Status.Refreshed = true
			coordinator.setObservation(observation)
			return observation.Status, nil
		case <-ctx.Done():
			return freshness.Status{}, ctx.Err()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	_ = receiveEvent(t, events)
	coordinator.setObservation(staleObservation("burst"))
	clock.advance(50 * time.Millisecond)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	for range 20 {
		clock.advance(50 * time.Millisecond)
	}
	close(release)
	if event := receiveEvent(t, events); event.Type != EventRefreshed {
		t.Fatalf("event = %#v", event)
	}
	if coordinator.maxConcurrent() != 1 || coordinator.ensureCalls() != 2 {
		t.Fatalf("max concurrent = %d, calls = %d", coordinator.maxConcurrent(), coordinator.ensureCalls())
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunCancellationInterruptsRefreshAndStopsTicker(t *testing.T) {
	t.Parallel()
	clock := newManualClock()
	coordinator := &fakeCoordinator{observation: currentObservation("initial")}
	started := make(chan struct{})
	coordinator.ensure = func(ctx context.Context, call int) (freshness.Status, error) {
		if call == 1 {
			return coordinator.snapshot().Status, nil
		}
		close(started)
		<-ctx.Done()
		return freshness.Status{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, coordinator, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	_ = receiveEvent(t, events)
	coordinator.setObservation(staleObservation("cancel-edit"))
	clock.advance(50 * time.Millisecond)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
	if !clock.stopped() {
		t.Fatal("ticker was not stopped after cancellation")
	}
}

func TestRunRealRepositoryCoalescesBurstAtomicSaveAndIgnoresIgnoredFiles(t *testing.T) {
	root := watchRepository(t)
	writeWatchFile(t, root, ".gitignore", "ignored.tmp\n")
	watchGit(t, root, "add", ".gitignore")
	watchGit(t, root, "commit", "-m", "ignore fixture")
	provider := &countingProvider{}
	manager := &freshness.Manager{Directory: root, Provider: provider, Command: "watch test"}
	clock := newManualClock()
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, manager, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	ready := receiveEvent(t, events)
	if ready.Type != EventReady || ready.Status == nil || !ready.Status.Refreshed || provider.calls() != 1 {
		t.Fatalf("initial event = %#v, calls = %d", ready, provider.calls())
	}

	writeWatchFile(t, root, "ignored.tmp", "not an input")
	clock.advance(50 * time.Millisecond)
	assertNoEvent(t, events)
	if provider.calls() != 1 {
		t.Fatalf("ignored file caused refresh: %d calls", provider.calls())
	}

	for index := range 8 {
		writeWatchFile(t, root, "temporary-save", strings.Repeat("x", index+1))
	}
	if err := os.Rename(filepath.Join(root, "temporary-save"), filepath.Join(root, "added.go")); err != nil {
		t.Fatal(err)
	}
	clock.advance(50 * time.Millisecond)
	refreshed := receiveEvent(t, events)
	if refreshed.Type != EventRefreshed || refreshed.Status == nil || !refreshed.Status.Refreshed || provider.calls() != 2 {
		t.Fatalf("burst event = %#v, calls = %d", refreshed, provider.calls())
	}
	clock.advance(50 * time.Millisecond)
	assertNoEvent(t, events)
	if provider.calls() != 2 {
		t.Fatalf("unchanged poll caused refresh: %d calls", provider.calls())
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunReportsProviderCancellationWhileParentContextRemainsLive(t *testing.T) {
	t.Parallel()
	for _, providerErr := range []error{context.Canceled, context.DeadlineExceeded} {
		providerErr := providerErr
		t.Run(providerErr.Error(), func(t *testing.T) {
			t.Parallel()
			clock := newManualClock()
			coordinator := &fakeCoordinator{observation: currentObservation("initial")}
			coordinator.ensure = func(_ context.Context, call int) (freshness.Status, error) {
				if call == 1 {
					return coordinator.snapshot().Status, nil
				}
				return freshness.Status{}, providerErr
			}
			ctx, cancel := context.WithCancel(context.Background())
			events, done := runAsync(ctx, coordinator, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
			_ = receiveEvent(t, events)
			coordinator.setObservation(staleObservation("provider-cancel-edit"))
			clock.advance(50 * time.Millisecond)
			event := receiveEvent(t, events)
			if event.Type != EventError || event.Error != providerErr.Error() {
				t.Fatalf("provider cancellation event = %#v", event)
			}
			select {
			case err := <-done:
				t.Fatalf("watch exited for provider-owned cancellation: %v", err)
			default:
			}
			cancel()
			if err := receiveDone(t, done); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRunRealRepositoryRecoversWhenWorktreeMutatesDuringRefresh(t *testing.T) {
	root := watchRepository(t)
	provider := &countingProvider{}
	manager := &freshness.Manager{Directory: root, Provider: provider, Command: "watch mutation test"}
	clock := newManualClock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, done := runAsync(ctx, manager, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	_ = receiveEvent(t, events)
	writeWatchFile(t, root, "main.go", "package first\n")
	provider.setMutation(func() { writeWatchFile(t, root, "main.go", "package second\n") })
	clock.advance(50 * time.Millisecond)
	failure := receiveEvent(t, events)
	if failure.Type != EventError || !strings.Contains(failure.Error, "repository state changed during provider refresh") {
		t.Fatalf("mutation failure = %#v", failure)
	}
	clock.advance(time.Millisecond)
	recovered := receiveEvent(t, events)
	if recovered.Type != EventRefreshed || recovered.Trigger != TriggerChange || provider.calls() != 3 {
		t.Fatalf("recovery = %#v, calls = %d", recovered, provider.calls())
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunUsesLinkedWorktreeFreshnessAndStorage(t *testing.T) {
	root := watchRepository(t)
	linked := filepath.Join(t.TempDir(), "linked")
	watchGit(t, root, "worktree", "add", "-b", "watch-linked", linked)
	t.Cleanup(func() { _ = exec.Command("git", "-C", root, "worktree", "remove", "--force", linked).Run() })
	manager := &freshness.Manager{Directory: linked, Provider: &countingProvider{}, Command: "linked watch test"}
	clock := newManualClock()
	ctx, cancel := context.WithCancel(context.Background())
	events, done := runAsync(ctx, manager, Options{PollInterval: 50 * time.Millisecond, Initial: true}, clock)
	ready := receiveEvent(t, events)
	if ready.Status == nil || ready.Status.WorktreeID == "" || ready.Status.WorktreeID == "main" || !strings.Contains(filepath.ToSlash(ready.Status.DatabasePath), "/worktrees/") {
		t.Fatalf("linked ready event = %#v", ready)
	}
	writeWatchFile(t, linked, "main.go", "package linked\n")
	clock.advance(50 * time.Millisecond)
	if event := receiveEvent(t, events); event.Type != EventRefreshed || event.Status == nil || event.Status.WorktreeID != ready.Status.WorktreeID {
		t.Fatalf("linked refresh event = %#v", event)
	}
	cancel()
	if err := receiveDone(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsBoundsAndSinkFailure(t *testing.T) {
	t.Parallel()
	coordinator := &fakeCoordinator{observation: currentObservation("state")}
	for _, interval := range []time.Duration{MinimumPollInterval - 1, MaximumPollInterval + 1} {
		if err := Run(context.Background(), coordinator, Options{PollInterval: interval}, func(Event) error { return nil }); err == nil {
			t.Fatalf("Run accepted interval %s", interval)
		}
	}
	want := errors.New("output unavailable")
	err := run(context.Background(), coordinator, Options{PollInterval: 100 * time.Millisecond}, func(Event) error { return want }, newManualClock())
	if !errors.Is(err, want) {
		t.Fatalf("sink error = %v", err)
	}
}

func TestBoundedErrorIsUTF8SafeAndBounded(t *testing.T) {
	t.Parallel()
	value := strings.Repeat("界", maximumErrorBytes) + string([]byte{0xff})
	got := boundedError(value)
	if len(got) > maximumErrorBytes || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("boundedError length=%d suffix=%t", len(got), strings.HasSuffix(got, "…"))
	}
}

type fakeCoordinator struct {
	mu          sync.Mutex
	observation freshness.Observation
	observe     func(int) (freshness.Observation, error)
	observes    int
	ensure      func(context.Context, int) (freshness.Status, error)
	calls       int
	active      int
	maximum     int
}

func (coordinator *fakeCoordinator) Observe(context.Context) (freshness.Observation, error) {
	coordinator.mu.Lock()
	coordinator.observes++
	call := coordinator.observes
	callback := coordinator.observe
	coordinator.mu.Unlock()
	if callback != nil {
		return callback(call)
	}
	return coordinator.snapshot(), nil
}

func (coordinator *fakeCoordinator) Ensure(ctx context.Context, _ bool) (freshness.Status, error) {
	coordinator.mu.Lock()
	coordinator.calls++
	call := coordinator.calls
	coordinator.active++
	if coordinator.active > coordinator.maximum {
		coordinator.maximum = coordinator.active
	}
	callback := coordinator.ensure
	coordinator.mu.Unlock()
	defer func() {
		coordinator.mu.Lock()
		coordinator.active--
		coordinator.mu.Unlock()
	}()
	if callback == nil {
		return coordinator.snapshot().Status, nil
	}
	return callback(ctx, call)
}

func (coordinator *fakeCoordinator) snapshot() freshness.Observation {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.observation
}

func (coordinator *fakeCoordinator) setObservation(observation freshness.Observation) {
	coordinator.mu.Lock()
	coordinator.observation = observation
	coordinator.mu.Unlock()
}

func (coordinator *fakeCoordinator) ensureCalls() int {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.calls
}

func (coordinator *fakeCoordinator) maxConcurrent() int {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	return coordinator.maximum
}

func currentObservation(token string) freshness.Observation {
	return freshness.Observation{Token: token, Status: freshness.Status{
		Initialized: true, Current: true, RepositoryIdentity: "github.com/example/repo",
		WorktreeID: "main", Generation: "sha256:generation",
	}}
}

func staleObservation(token string) freshness.Observation {
	observation := currentObservation(token)
	observation.Status.Current = false
	observation.Status.Reason = "repository state changed"
	return observation
}

func identifiedObservation(token, generation string, current bool) freshness.Observation {
	status := freshness.Status{
		Initialized: true, Current: current, RepositoryIdentity: "github.com/example/repo",
		WorktreeID: "linked-a", Commit: "commit-a", Tree: "tree-a", Dirty: true,
		ChangeCount: 1,
	}
	if current {
		status.Generation = generation
	} else {
		status.Reason = "repository state changed"
	}
	return freshness.Observation{Token: token, Status: status}
}

type manualClock struct {
	mu      sync.Mutex
	now     time.Time
	ticker  *manualTicker
	created chan struct{}
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_700_000_000, 0), created: make(chan struct{})}
}

func (clock *manualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *manualClock) NewTicker(time.Duration) ticker {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.ticker = &manualTicker{ticks: make(chan time.Time, 1)}
	close(clock.created)
	return clock.ticker
}

func (clock *manualClock) advance(duration time.Duration) {
	select {
	case <-clock.created:
	case <-time.After(3 * time.Second):
		panic("watch ticker was not created")
	}
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	now := clock.now
	ticker := clock.ticker
	clock.mu.Unlock()
	select {
	case ticker.ticks <- now:
	default:
	}
}

func (clock *manualClock) stopped() bool {
	clock.mu.Lock()
	ticker := clock.ticker
	clock.mu.Unlock()
	if ticker == nil {
		return false
	}
	ticker.mu.Lock()
	defer ticker.mu.Unlock()
	return ticker.stopped
}

type manualTicker struct {
	mu      sync.Mutex
	ticks   chan time.Time
	stopped bool
}

func (ticker *manualTicker) Channel() <-chan time.Time { return ticker.ticks }
func (ticker *manualTicker) Stop() {
	ticker.mu.Lock()
	ticker.stopped = true
	ticker.mu.Unlock()
}

func runAsync(ctx context.Context, coordinator Coordinator, options Options, clock clock) (<-chan Event, <-chan error) {
	events := make(chan Event, 64)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, coordinator, options, func(event Event) error { events <- event; return nil }, clock)
	}()
	return events, done
}

func receiveEvent(t *testing.T, events <-chan Event) Event {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for watch event")
		return Event{}
	}
}

func assertNoEvent(t *testing.T, events <-chan Event) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected watch event %#v", event)
	case <-time.After(25 * time.Millisecond):
	}
}

func receiveDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("watch did not stop")
		return nil
	}
}

type countingProvider struct {
	mu       sync.Mutex
	count    int
	mutation func()
}

func (*countingProvider) ID() freshness.ProviderID {
	return freshness.ProviderID{Name: "watch-fixture", Version: "1"}
}

func (provider *countingProvider) Refresh(_ context.Context, _ freshness.Request) (freshness.Result, error) {
	provider.mu.Lock()
	provider.count++
	mutation := provider.mutation
	provider.mutation = nil
	provider.mu.Unlock()
	if mutation != nil {
		mutation()
	}
	return freshness.Result{}, nil
}

func (provider *countingProvider) calls() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return provider.count
}

func (provider *countingProvider) setMutation(mutation func()) {
	provider.mu.Lock()
	provider.mutation = mutation
	provider.mu.Unlock()
}

func watchRepository(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repo")
	watchGit(t, "", "init", "--initial-branch=main", root)
	watchGit(t, root, "config", "user.email", "weave@example.test")
	watchGit(t, root, "config", "user.name", "Weave Test")
	writeWatchFile(t, root, "main.go", "package fixture\n")
	watchGit(t, root, "add", ".")
	watchGit(t, root, "commit", "-m", "initial")
	return root
}

func watchGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func writeWatchFile(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
