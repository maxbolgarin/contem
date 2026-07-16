package contem_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maxbolgarin/contem"
)

// Shutdown functions must receive a usable (not canceled) context with a deadline,
// even though the underlying signal context is already canceled at shutdown time.
func TestShutdownFuncContextUsable(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []contem.Option
	}{
		{"parallel", nil},
		{"no_parallel", []contem.Option{contem.NoParallel()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]contem.Option{contem.WithShutdownTimeout(5 * time.Second)}, tc.opts...)
			ctx := contem.New(opts...)
			ctx.SetValue("key", "value")

			var (
				canceled    atomic.Bool
				hasDeadline atomic.Bool
				hasValue    atomic.Bool
			)
			ctx.Add(func(c context.Context) error {
				canceled.Store(c.Err() != nil)
				_, ok := c.Deadline()
				hasDeadline.Store(ok)
				hasValue.Store(c.Value("key") == "value")
				return nil
			})

			ctx.Cancel() // simulate an interrupt signal before shutdown
			if err := ctx.Shutdown(); err != nil {
				t.Fatalf("shutdown error: %v", err)
			}

			if canceled.Load() {
				t.Error("shutdown function received an already canceled context")
			}
			if !hasDeadline.Load() {
				t.Error("shutdown function context has no deadline")
			}
			if !hasValue.Load() {
				t.Error("shutdown function context lost values of the underlying context")
			}
		})
	}
}

// File closers must get at least timeout/5 of the budget even if the
// shutdown functions consumed the whole shutdown timeout.
func TestFileClosersGetTimeBudget(t *testing.T) {
	ctx := contem.New(contem.WithShutdownTimeout(500 * time.Millisecond))

	ctx.Add(func(context.Context) error {
		time.Sleep(2 * time.Second) // exhaust the whole shutdown budget
		return nil
	})

	var closed atomic.Bool
	ctx.AddFile(&testFile{
		syncFunc: func() error {
			time.Sleep(50 * time.Millisecond) // well within the timeout/5 floor (100ms)
			return nil
		},
		closeFunc: func() error {
			closed.Store(true)
			return nil
		},
	})

	err := ctx.Shutdown()
	if err == nil || !strings.Contains(err.Error(), "shutdown: timeout") {
		t.Fatalf("expected shutdown timeout error, got: %v", err)
	}
	if strings.Contains(err.Error(), "close files") {
		t.Errorf("file closers should have had time to finish, got: %v", err)
	}
	if !closed.Load() {
		t.Error("file was not closed")
	}
}

// SetValue must be safe to call concurrently with the context.Context methods (run with -race).
func TestSetValueConcurrentWithReads(t *testing.T) {
	ctx := contem.New()
	defer ctx.Shutdown()

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ctx.SetValue(fmt.Sprintf("key-%d-%d", i, j), j)
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = ctx.Value("key-0-0")
				_ = ctx.Err()
				_, _ = ctx.Deadline()
				select {
				case <-ctx.Done():
				default:
				}
			}
		}()
	}
	wg.Wait()
}

func TestSetValueNilKey(t *testing.T) {
	ctx := contem.New()
	defer ctx.Shutdown()

	// Catch the panic here: a deferred ctx.Shutdown() would silently swallow it
	var panicked any
	func() {
		defer func() { panicked = recover() }()
		if res := ctx.SetValue(nil, "value"); res != ctx {
			t.Error("SetValue with nil key should return the same context")
		}
	}()

	if panicked != nil {
		t.Errorf("SetValue with nil key panicked: %v", panicked)
	}
}

// A shutdown function that registers more cleanup must not deadlock Shutdown
// (before the fix this was a guaranteed self-deadlock in NoParallel mode).
func TestReentrantAddDuringShutdown(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []contem.Option
	}{
		{"parallel", nil},
		{"no_parallel", []contem.Option{contem.NoParallel()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := append([]contem.Option{contem.WithShutdownTimeout(time.Second)}, tc.opts...)
			ctx := contem.New(opts...)

			ctx.Add(func(context.Context) error {
				ctx.AddClose(func() error { return nil })
				ctx.SetValue("late", "value")
				return nil
			})

			done := make(chan error, 1)
			go func() { done <- ctx.Shutdown() }()

			select {
			case err := <-done:
				if err != nil {
					t.Errorf("shutdown error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Shutdown deadlocked")
			}
		})
	}
}

// Functions added after Shutdown has started are ignored.
func TestAddAfterShutdown(t *testing.T) {
	ctx := contem.New()

	if err := ctx.Shutdown(); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}

	var called atomic.Bool
	ctx.Add(func(context.Context) error {
		called.Store(true)
		return nil
	})
	ctx.AddClose(func() error {
		called.Store(true)
		return nil
	})

	if err := ctx.Shutdown(); err != nil {
		t.Fatalf("second shutdown error: %v", err)
	}
	if called.Load() {
		t.Error("function added after shutdown must not be called")
	}
}

// A concurrent Shutdown call must block until the winning call finishes its cleanup,
// so patterns like AutoShutdown + `defer ctx.Shutdown()` in main cannot exit mid-cleanup.
func TestConcurrentShutdownBlocksUntilComplete(t *testing.T) {
	ctx := contem.New()

	release := make(chan struct{})
	var finished atomic.Bool
	ctx.Add(func(context.Context) error {
		<-release
		finished.Store(true)
		return nil
	})

	first := make(chan struct{})
	go func() {
		ctx.Shutdown()
		close(first)
	}()
	time.Sleep(50 * time.Millisecond) // let the first Shutdown claim the work

	second := make(chan struct{})
	go func() {
		ctx.Shutdown()
		close(second)
	}()

	select {
	case <-second:
		t.Fatal("second Shutdown returned before the first one completed")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)

	select {
	case <-second:
	case <-time.After(5 * time.Second):
		t.Fatal("second Shutdown did not return after the first one completed")
	}
	<-first

	if !finished.Load() {
		t.Error("shutdown function did not finish")
	}
}

// errors.Is must work through the error returned by Shutdown.
func TestShutdownErrorSupportsErrorsIs(t *testing.T) {
	sentinel := errors.New("sentinel")

	ctx := contem.New()
	ctx.Add(func(context.Context) error {
		return fmt.Errorf("wrap: %w", sentinel)
	})
	ctx.Add(func(context.Context) error {
		return errors.New("other error")
	})

	err := ctx.Shutdown()
	if err == nil {
		t.Fatal("expected error from shutdown")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is should find the sentinel through the joined error, got: %v", err)
	}
}
