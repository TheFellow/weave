// Package watch provides optional foreground warming of the authoritative
// query-driven freshness pipeline.
package watch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TheFellow/weave/internal/freshness"
	"github.com/TheFellow/weave/internal/repository"
)

const (
	Schema              = "weave.watch-event/v1"
	DefaultPollInterval = 750 * time.Millisecond
	MinimumPollInterval = 50 * time.Millisecond
	MaximumPollInterval = 5 * time.Minute
	maximumRetryBackoff = 30 * time.Second
	maximumErrorBytes   = 8 << 10
)

// EventType identifies one stable newline-delimited watch record.
type EventType string

const (
	EventReady     EventType = "ready"
	EventRefreshed EventType = "refreshed"
	EventError     EventType = "error"
)

// Trigger explains why an event-producing refresh was attempted.
type Trigger string

const (
	TriggerInitial Trigger = "initial"
	TriggerChange  Trigger = "change"
	TriggerRetry   Trigger = "retry"
)

// Event is the versioned machine contract emitted by a foreground warmer.
// Sequence is local to one invocation and increases only for emitted records.
type Event struct {
	Schema      string            `json:"schema"`
	Sequence    uint64            `json:"sequence"`
	Type        EventType         `json:"type"`
	Trigger     Trigger           `json:"trigger,omitempty"`
	Observation string            `json:"observation,omitempty"`
	Status      *freshness.Status `json:"status,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// Options controls polling. Initial requests one non-forced refresh before the
// first ready event. PollInterval is also the edit coalescing window.
type Options struct {
	PollInterval time.Duration
	Initial      bool
}

// Coordinator is the existing authoritative per-worktree freshness boundary.
type Coordinator interface {
	Observe(context.Context) (freshness.Observation, error)
	Ensure(context.Context, bool) (freshness.Status, error)
}

// Sink receives ordered watch events synchronously.
type Sink func(Event) error

// Service is implemented by applications that can warm a managed worktree.
type Service interface {
	Watch(context.Context, Options, Sink) error
}

// Run polls and warms coordinator until cancellation. Cancellation is a normal
// foreground shutdown and returns nil.
func Run(ctx context.Context, coordinator Coordinator, options Options, sink Sink) error {
	return run(ctx, coordinator, options, sink, systemClock{})
}

type retryState struct {
	key         string
	failures    int
	nextAttempt time.Time
	refresh     bool
}

func (state *retryState) clear() { *state = retryState{} }

func (state *retryState) failed(key string, now time.Time, interval time.Duration, refresh bool) {
	if state.key != key {
		state.key = key
		state.failures = 0
	}
	state.refresh = refresh
	state.failures++
	delay := interval
	for step := 1; step < state.failures && delay < maximumRetryBackoff; step++ {
		if delay > maximumRetryBackoff/2 {
			delay = maximumRetryBackoff
			break
		}
		delay *= 2
	}
	if delay > maximumRetryBackoff {
		delay = maximumRetryBackoff
	}
	state.nextAttempt = now.Add(delay)
}

func (state retryState) waiting(key string, now time.Time) bool {
	return state.key == key && now.Before(state.nextAttempt)
}

func run(ctx context.Context, coordinator Coordinator, options Options, sink Sink, clock clock) error {
	if coordinator == nil {
		return errors.New("watch freshness coordinator is unavailable")
	}
	if sink == nil {
		return errors.New("watch event sink is unavailable")
	}
	if options.PollInterval < MinimumPollInterval || options.PollInterval > MaximumPollInterval {
		return fmt.Errorf("watch poll interval must be between %s and %s", MinimumPollInterval, MaximumPollInterval)
	}

	var sequence uint64
	emit := func(event Event) error {
		sequence++
		event.Schema = Schema
		event.Sequence = sequence
		if event.Type == EventError {
			event.Error = boundedError(event.Error)
		}
		return sink(event)
	}
	var retry retryState
	readyEmitted := false
	emitCompletion := func(status freshness.Status, observation freshness.Observation, observeErr error, trigger Trigger) error {
		eventType := EventRefreshed
		if !readyEmitted {
			eventType = EventReady
		}
		event := Event{Type: eventType, Trigger: trigger, Status: statusPointer(status)}
		if observeErr == nil && completionMatches(status, observation.Status) {
			event.Observation = observation.Token
		}
		if err := emit(event); err != nil {
			return err
		}
		readyEmitted = true
		return nil
	}

	if options.Initial {
		status, err := coordinator.Ensure(ctx, false)
		if err == nil {
			observation, observeErr := coordinator.Observe(ctx)
			if emitErr := emitCompletion(status, observation, observeErr, TriggerInitial); emitErr != nil {
				return emitErr
			}
			if canceled(ctx) {
				return nil
			}
			if errors.Is(observeErr, repository.ErrNotRepository) {
				return observeErr
			}
			if observeErr != nil {
				if emitErr := emit(Event{Type: EventError, Trigger: TriggerInitial, Error: "observe worktree after initial refresh: " + observeErr.Error()}); emitErr != nil {
					return emitErr
				}
				retry.failed(retryKey("", observeErr), clock.Now(), options.PollInterval, false)
			}
		} else {
			if canceled(ctx) {
				return nil
			}
			if errors.Is(err, repository.ErrNotRepository) {
				return err
			}
			observation, observeErr := coordinator.Observe(ctx)
			if canceled(ctx) {
				return nil
			}
			if errors.Is(observeErr, repository.ErrNotRepository) {
				return observeErr
			}
			var observedStatus *freshness.Status
			if observeErr == nil {
				observedStatus = statusPointer(observation.Status)
			}
			key := observation.Token
			if emitErr := emit(Event{Type: EventError, Trigger: TriggerInitial, Observation: observation.Token, Status: observedStatus, Error: err.Error()}); emitErr != nil {
				return emitErr
			}
			retry.failed(key, clock.Now(), options.PollInterval, true)
		}
	} else {
		observation, err := coordinator.Observe(ctx)
		if canceled(ctx) {
			return nil
		}
		if err != nil {
			if errors.Is(err, repository.ErrNotRepository) {
				return err
			}
			if emitErr := emit(Event{Type: EventError, Trigger: TriggerInitial, Error: err.Error()}); emitErr != nil {
				return emitErr
			}
			retry.failed(retryKey(observation.Token, err), clock.Now(), options.PollInterval, false)
		} else {
			if err := emit(Event{Type: EventReady, Observation: observation.Token, Status: statusPointer(observation.Status)}); err != nil {
				return err
			}
			readyEmitted = true
		}
	}

	ticker := clock.NewTicker(options.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.Channel():
			observation, err := coordinator.Observe(ctx)
			if canceled(ctx) {
				return nil
			}
			if err != nil {
				if errors.Is(err, repository.ErrNotRepository) {
					return err
				}
				if retry.refresh {
					if retry.waiting(retry.key, now) {
						continue
					}
					if emitErr := emit(Event{Type: EventError, Trigger: TriggerRetry, Error: err.Error()}); emitErr != nil {
						return emitErr
					}
					retry.failed(retry.key, now, options.PollInterval, true)
					continue
				}
				key := retryKey(observation.Token, err)
				if retry.waiting(key, now) {
					continue
				}
				trigger := TriggerChange
				if retry.key == key {
					trigger = TriggerRetry
				}
				if emitErr := emit(Event{Type: EventError, Trigger: trigger, Error: err.Error()}); emitErr != nil {
					return emitErr
				}
				retry.failed(key, now, options.PollInterval, false)
				continue
			}
			// Any failed authoritative Ensure remains pending until another Ensure
			// succeeds. A different token bypasses the old token's backoff, but a
			// cheap current observation must not clear a generation-verification
			// failure it cannot itself detect.
			retryRefresh := retry.refresh
			if observation.Status.Current && !retryRefresh {
				retry.clear()
				if !readyEmitted {
					if err := emit(Event{Type: EventReady, Trigger: TriggerRetry, Observation: observation.Token, Status: statusPointer(observation.Status)}); err != nil {
						return err
					}
					readyEmitted = true
				}
				continue
			}
			key := observation.Token
			if retry.waiting(key, now) {
				continue
			}
			trigger := TriggerChange
			if (retry.refresh && retry.key == "") || (retry.key != "" && retry.key == key) {
				trigger = TriggerRetry
			}
			status, refreshErr := coordinator.Ensure(ctx, false)
			if canceled(ctx) {
				return nil
			}
			if refreshErr != nil {
				if errors.Is(refreshErr, repository.ErrNotRepository) {
					return refreshErr
				}
				if emitErr := emit(Event{Type: EventError, Trigger: trigger, Observation: observation.Token, Status: statusPointer(observation.Status), Error: refreshErr.Error()}); emitErr != nil {
					return emitErr
				}
				retry.failed(observation.Token, now, options.PollInterval, true)
				continue
			}
			retry.clear()
			if !status.Refreshed && !readyEmitted {
				if err := emitCompletion(status, observation, nil, trigger); err != nil {
					return err
				}
			}
			if status.Refreshed {
				current, observeErr := coordinator.Observe(ctx)
				if err := emitCompletion(status, current, observeErr, trigger); err != nil {
					return err
				}
				if canceled(ctx) {
					return nil
				}
				if errors.Is(observeErr, repository.ErrNotRepository) {
					return observeErr
				}
				if observeErr != nil {
					if emitErr := emit(Event{Type: EventError, Trigger: trigger, Error: "observe worktree after refresh: " + observeErr.Error()}); emitErr != nil {
						return emitErr
					}
					retry.failed(retryKey("", observeErr), now, options.PollInterval, false)
				}
			}
		}
	}
}

func statusPointer(status freshness.Status) *freshness.Status { return &status }

func completionMatches(status freshness.Status, observed freshness.Status) bool {
	return status.RepositoryIdentity == observed.RepositoryIdentity &&
		status.WorktreeID == observed.WorktreeID &&
		status.Commit == observed.Commit &&
		status.Tree == observed.Tree &&
		status.Dirty == observed.Dirty &&
		status.ChangeCount == observed.ChangeCount &&
		status.Generation != "" && status.Generation == observed.Generation
}

func retryKey(observation string, err error) string {
	if observation != "" {
		return observation
	}
	if err == nil {
		return "unknown-observation"
	}
	return "observation-error\x00" + boundedError(err.Error())
}

func boundedError(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maximumErrorBytes {
		return value
	}
	const suffix = "…"
	limit := maximumErrorBytes - len(suffix)
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + suffix
}

func canceled(ctx context.Context) bool {
	return ctx.Err() != nil
}

type ticker interface {
	Channel() <-chan time.Time
	Stop()
}

type clock interface {
	Now() time.Time
	NewTicker(time.Duration) ticker
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
func (systemClock) NewTicker(interval time.Duration) ticker {
	return systemTicker{Ticker: time.NewTicker(interval)}
}

type systemTicker struct{ *time.Ticker }

func (ticker systemTicker) Channel() <-chan time.Time { return ticker.C }
