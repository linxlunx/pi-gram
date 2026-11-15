package queue

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestQueueSequentialOrder(t *testing.T) {
	q := New(16, nil)
	var mu sync.Mutex
	var order []int
	done := make(chan struct{})
	go func() {
		q.Run(context.Background())
		close(done)
	}()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		if err := q.Submit(func(ctx context.Context) {
			defer wg.Done()
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	wg.Wait()

	mu.Lock()
	for i, v := range order {
		if v != i {
			t.Fatalf("order[%d] = %d, want %d (sequential execution violated)", i, v, i)
		}
	}
	mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	go func() { q.Run(ctx) }()
}

func TestQueueFull(t *testing.T) {
	release := make(chan struct{})
	q := New(1, nil)
	// Occupy the worker with a blocked task.
	if err := q.Submit(func(ctx context.Context) { <-release }); err != nil {
		t.Fatalf("submit: %v", err)
	}
	go q.Run(context.Background())
	// Give the worker time to pick up the first task so the channel is
	// the backlog only.
	time.Sleep(100 * time.Millisecond)

	// Fill the backlog.
	if err := q.Submit(func(ctx context.Context) {}); err != nil {
		t.Fatalf("backlog submit should succeed: %v", err)
	}
	if err := q.Submit(func(ctx context.Context) {}); err == nil {
		t.Fatal("submit should fail with ErrQueueFull")
	} else if err != ErrQueueFull {
		t.Fatalf("err = %v, want ErrQueueFull", err)
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
}

func TestQueueTaskPanicRecovered(t *testing.T) {
	q := New(4, nil)
	go q.Run(context.Background())
	if err := q.Submit(func(ctx context.Context) { panic("boom") }); err != nil {
		t.Fatalf("submit: %v", err)
	}
	ok := make(chan struct{})
	if err := q.Submit(func(ctx context.Context) { close(ok) }); err != nil {
		t.Fatalf("submit after panic: %v", err)
	}
	select {
	case <-ok:
	case <-time.After(2 * time.Second):
		t.Fatal("queue stopped serving tasks after a panic")
	}
}
