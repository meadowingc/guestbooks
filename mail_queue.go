package main

import (
	"context"
	"errors"
	"log"
	"sync"
)

var ErrMailShuttingDown = errors.New("email delivery is shutting down")

type mailTaskQueue struct {
	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	active  int
	closing bool
	done    chan struct{}
}

func newMailTaskQueue() *mailTaskQueue {
	ctx, cancel := context.WithCancel(context.Background())
	return &mailTaskQueue{ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

var applicationMail = newMailTaskQueue()

func queueMail(description string, send func(context.Context) error) error {
	return applicationMail.enqueue(description, send)
}

func (queue *mailTaskQueue) enqueue(description string, send func(context.Context) error) error {
	if send == nil {
		return errors.New("mail task requires a sender")
	}
	snapshot, err := mailSnapshotForContext(context.Background())
	if err != nil {
		return err
	}
	if snapshot.config.provider == "none" {
		return ErrMailDisabled
	}
	return queue.queue(description, func(ctx context.Context) error {
		return send(context.WithValue(ctx, mailSnapshotKey{}, snapshot))
	})
}

func shutdownMail(ctx context.Context) error { return applicationMail.shutdown(ctx) }

func (queue *mailTaskQueue) queue(description string, send func(context.Context) error) error {
	if send == nil {
		return errors.New("mail task requires a sender")
	}
	queue.mu.Lock()
	if queue.closing {
		queue.mu.Unlock()
		return ErrMailShuttingDown
	}
	queue.active++
	queue.mu.Unlock()
	go func() {
		defer func() {
			queue.mu.Lock()
			defer queue.mu.Unlock()
			queue.active--
			if queue.closing && queue.active == 0 {
				close(queue.done)
			}
		}()
		ctx, cancel := context.WithTimeout(queue.ctx, mailSendTimeout)
		defer cancel()
		if err := send(ctx); err != nil {
			log.Printf("Mail task %s: %s", description, safeMailError(err))
		}
	}()
	return nil
}

func (queue *mailTaskQueue) shutdown(ctx context.Context) error {
	queue.mu.Lock()
	if !queue.closing {
		queue.closing = true
		if queue.active == 0 {
			close(queue.done)
		}
	}
	queue.mu.Unlock()
	select {
	case <-queue.done:
		queue.cancel()
		return nil
	case <-ctx.Done():
		queue.cancel()
		return ctx.Err()
	}
}
