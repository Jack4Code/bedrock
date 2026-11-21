package bedrock

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// newTestJobRunner registers jobs on a runner that logs into a buffer. The
// cron scheduler is never started: tests fire ticks by hand through tick, so
// they do not wait on the clock.
func newTestJobRunner(t *testing.T, jobs ...Job) (*jobRunner, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	jr, err := newJobRunner(context.Background(), jobs, slog.New(slog.NewTextHandler(logs, nil)))
	if err != nil {
		t.Fatalf("newJobRunner: %v", err)
	}
	return jr, logs
}

// tick runs the i'th registered job once, exactly as the scheduler would.
func tick(t *testing.T, jr *jobRunner, i int) {
	t.Helper()
	entries := jr.c.Entries()
	if i >= len(entries) {
		t.Fatalf("job %d not registered (have %d)", i, len(entries))
	}
	entries[i].WrappedJob.Run()
}

func TestJobPanicIsRecoveredAndLogged(t *testing.T) {
	jr, logs := newTestJobRunner(t, Job{
		Schedule: "@hourly",
		Handler:  func(context.Context) error { panic("boom") },
	})

	tick(t, jr, 0) // would end the test binary if the panic escaped

	out := logs.String()
	for _, want := range []string{"level=ERROR", `msg="job panicked"`, "schedule=@hourly", "panic=boom", "stack="} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
	// With no OnError, the default handler must not log the panic a second time.
	if strings.Contains(out, `msg="job error"`) {
		t.Errorf("panic logged twice:\n%s", out)
	}
}

func TestJobPanicIsPassedToOnError(t *testing.T) {
	var got error
	jr, _ := newTestJobRunner(t, Job{
		Schedule: "@hourly",
		Handler:  func(context.Context) error { panic("boom") },
		OnError:  func(err error) { got = err },
	})

	tick(t, jr, 0)

	if !errors.Is(got, ErrJobPanicked) {
		t.Fatalf("OnError got %v, want an error wrapping ErrJobPanicked", got)
	}
	if !strings.Contains(got.Error(), "boom") {
		t.Errorf("OnError error %q does not carry the panic value", got)
	}
}

func TestJobKeepsScheduleAfterPanic(t *testing.T) {
	runs := 0
	jr, _ := newTestJobRunner(t, Job{
		Schedule: "@hourly",
		Handler: func(context.Context) error {
			runs++
			if runs == 1 {
				panic("first run fails")
			}
			return nil
		},
	})

	tick(t, jr, 0)
	tick(t, jr, 0)

	if runs != 2 {
		t.Fatalf("handler ran %d times, want 2", runs)
	}
}

func TestJobOnErrorPanicIsRecovered(t *testing.T) {
	jr, logs := newTestJobRunner(t, Job{
		Schedule: "@hourly",
		Handler:  func(context.Context) error { return errors.New("failed") },
		OnError:  func(error) { panic("handler of the handler") },
	})

	tick(t, jr, 0)

	if out := logs.String(); !strings.Contains(out, `msg="job error handler panicked"`) {
		t.Errorf("OnError panic not logged:\n%s", out)
	}
}

func TestJobErrorUsesDefaultLogger(t *testing.T) {
	jr, logs := newTestJobRunner(t, Job{
		Schedule: "@daily",
		Handler:  func(context.Context) error { return errors.New("db unreachable") },
	})

	tick(t, jr, 0)

	out := logs.String()
	for _, want := range []string{`msg="job error"`, "schedule=@daily", `err="db unreachable"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}
