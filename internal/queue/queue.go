// Package queue implements a simple sequential task queue. Messages for
// the (initially single) Pi session are processed one at a time so that
// concurrent prompts never interleave.
package queue

import (
	"context"
	"errors"
	"log/slog"
)

// Task is a unit of work executed by the queue worker.
type Task func(ctx context.Context)

// ErrQueueFull is returned by Submit when the queue is full.
var ErrQueueFull = errors.New("queue full")

// Queue processes tasks strictly sequentially in submission order.
type Queue struct {
	tasks  chan Task
	logger *slog.Logger
}

// New creates a queue with the given maximum backlog size.
func New(size int, logger *slog.Logger) *Queue {
	if size <= 0 {
		size = 32
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Queue{
		tasks:  make(chan Task, size),
		logger: logger,
	}
}

// Submit enqueues a task without blocking. It returns ErrQueueFull if
// the backlog is full.
func (q *Queue) Submit(t Task) error {
	select {
	case q.tasks <- t:
		return nil
	default:
		return ErrQueueFull
	}
}

// Len returns the number of currently queued (not yet started) tasks.
func (q *Queue) Len() int {
	return len(q.tasks)
}

// Run processes tasks sequentially until ctx is cancelled. It is
// intended to be started in its own goroutine.
func (q *Queue) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			q.logger.Info("queue worker stopped", "backlog", q.Len())
			return
		case t := <-q.tasks:
			func() {
				defer func() {
					if r := recover(); r != nil {
						q.logger.Error("queue task panicked", "panic", r)
					}
				}()
				t(ctx)
			}()
		}
	}
}
