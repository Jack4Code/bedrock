package bedrock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/robfig/cron/v3"
)

// Job defines a scheduled background task.
type Job struct {
	// Schedule is a standard cron expression or shorthand (@yearly, @monthly, @weekly, @daily, @hourly).
	Schedule string
	// Handler is called on each scheduled tick. A non-nil error triggers OnError.
	//
	// A panic in Handler is recovered: it is logged at Error with its stack
	// trace, passed to OnError as an error wrapping ErrJobPanicked, and the job
	// keeps its schedule. It does not crash the process.
	Handler func(ctx context.Context) error
	// OnError is called when Handler returns an error or panics. Defaults to
	// logging if nil. A panic in OnError is recovered and logged too.
	OnError func(err error)
}

// ErrJobPanicked is wrapped by the error passed to a Job's OnError when its
// Handler panicked. Check for it with errors.Is.
var ErrJobPanicked = errors.New("bedrock: job panicked")

// JobsProvider is an optional interface apps can implement to register scheduled jobs.
// Bedrock will start and stop the job runner as part of the server lifecycle.
type JobsProvider interface {
	Jobs() []Job
}

type jobRunner struct {
	c *cron.Cron
}

func newJobRunner(ctx context.Context, jobs []Job, logger *slog.Logger) (*jobRunner, error) {
	c := cron.New()
	for _, j := range jobs {
		j := j
		_, err := c.AddFunc(j.Schedule, func() {
			runJob(ctx, j, logger)
		})
		if err != nil {
			return nil, fmt.Errorf("invalid job schedule %q: %w", j.Schedule, err)
		}
	}
	return &jobRunner{c: c}, nil
}

// runJob runs one tick of j. Neither a panic in Handler nor one in OnError
// escapes: robfig/cron v3 does not recover panics in jobs, so one that escaped
// here would end the process.
func runJob(ctx context.Context, j Job, logger *slog.Logger) {
	err := callHandler(ctx, j, logger)
	if err == nil {
		return
	}
	if j.OnError == nil {
		// A panic has already been logged with its stack trace; logging it
		// again here would only repeat it.
		if !errors.Is(err, ErrJobPanicked) {
			logger.Error("job error", "schedule", j.Schedule, "err", err)
		}
		return
	}
	defer func() {
		if p := recover(); p != nil {
			logger.Error("job error handler panicked",
				"schedule", j.Schedule, "panic", p, "stack", string(debug.Stack()))
		}
	}()
	j.OnError(err)
}

// callHandler runs j.Handler, turning a panic into an error wrapping
// ErrJobPanicked after logging it with its stack trace.
func callHandler(ctx context.Context, j Job, logger *slog.Logger) (err error) {
	defer func() {
		if p := recover(); p != nil {
			logger.Error("job panicked",
				"schedule", j.Schedule, "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("%w: %v", ErrJobPanicked, p)
		}
	}()
	return j.Handler(ctx)
}

func (jr *jobRunner) Start() {
	jr.c.Start()
}

func (jr *jobRunner) Stop() {
	<-jr.c.Stop().Done()
}
