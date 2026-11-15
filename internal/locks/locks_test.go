package locks

import (
	"context"
	"testing"
	"time"
)

func TestReentrantAcquire(t *testing.T) {
	manager := NewManager()
	ctx, release, err := manager.Acquire(context.Background(), "repo")
	if err != nil {
		t.Fatal(err)
	}
	nextCtx, releaseAgain, err := manager.Acquire(ctx, "repo")
	if err != nil || nextCtx == nil {
		t.Fatalf("nested acquire failed: %v", err)
	}
	releaseAgain()
	release()

	done := make(chan struct{})
	go func() {
		_, release, _ := manager.Acquire(context.Background(), "repo")
		release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("lock was not released")
	}
}

func TestDifferentReposRunInParallel(t *testing.T) {
	manager := NewManager()
	_, releaseA, _ := manager.Acquire(context.Background(), "a")
	defer releaseA()

	done := make(chan struct{})
	go func() {
		_, releaseB, _ := manager.Acquire(context.Background(), "b")
		releaseB()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("different repositories must not block each other")
	}
}

func TestAcquireHonorsCancellation(t *testing.T) {
	manager := NewManager()
	_, release, _ := manager.Acquire(context.Background(), "repo")
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := manager.Acquire(ctx, "repo"); err == nil {
		t.Fatal("expected context error")
	}
}
