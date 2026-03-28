package bedrock

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/robfig/cron/v3"
)

// Job defines a scheduled background task.
type Job struct {
	// Schedule is a standard cron expression or shorthand (@yearly, @monthly, @weekly, @daily, @hourly).
	Schedule string
	// Handler is called on each scheduled tick. A non-nil error triggers OnError.
	Handler func(ctx context.Context) error
	// OnError is called when Handler returns an error. Defaults to logging if nil.
	OnError func(err error)
}

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
		onError := j.OnError
		if onError == nil {
			onError = func(err error) {
				logger.Error("job error", "schedule", j.Schedule, "err", err)
			}
		}
		_, err := c.AddFunc(j.Schedule, func() {
			if err := j.Handler(ctx); err != nil {
				onError(err)
			}
		})
		if err != nil {
			return nil, fmt.Errorf("invalid job schedule %q: %w", j.Schedule, err)
		}
	}
	return &jobRunner{c: c}, nil
}

func (jr *jobRunner) Start() {
	jr.c.Start()
}

func (jr *jobRunner) Stop() {
	<-jr.c.Stop().Done()
}
