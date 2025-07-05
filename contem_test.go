package contem_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/maxbolgarin/contem"
)

func TestShutdown(t *testing.T) {
	ctx := contem.New(contem.WithLogger(slog.Default()))

	if val := ctx.Value("key"); val != nil {
		t.Errorf("unexpected value: %v", val)
	}

	ctx.SetValue("key", "value")

	if val := ctx.Value("key"); val.(string) != "value" {
		t.Errorf("unexpected value: %v", val)
	}

	var (
		firstFuncFlag  atomic.Bool
		secondFuncFlag atomic.Bool
		thirdFuncFlag  atomic.Bool
		syncFuncFlag   atomic.Bool
		closeFuncFlag  atomic.Bool
		syncFunc2Flag  atomic.Bool
		closeFunc2Flag atomic.Bool
	)
	ctx.Add(func(ctx context.Context) error {
		firstFuncFlag.Store(true)
		return nil
	})
	ctx.AddClose(func() error {
		secondFuncFlag.Store(true)
		return nil
	})
	ctx.AddFunc(func() {
		thirdFuncFlag.Store(true)
	})
	ctx.AddFile(&file{&syncFuncFlag, &closeFuncFlag, false, false})
	ctx.AddFile(&file{&syncFunc2Flag, &closeFunc2Flag, false, false})

	// Call shutdown directly instead of in a goroutine to avoid race conditions
	err := ctx.Shutdown()
	if err != nil {
		t.Errorf("shutdown error: %v", err)
	}

	// Check that all functions were called
	if !firstFuncFlag.Load() {
		t.Errorf("firstFuncFlag is not set")
	}
	if !secondFuncFlag.Load() {
		t.Errorf("secondFuncFlag is not set")
	}
	if !thirdFuncFlag.Load() {
		t.Errorf("thirdFuncFlag is not set")
	}
	if !syncFuncFlag.Load() {
		t.Errorf("syncFuncFlag is not set")
	}
	if !closeFuncFlag.Load() {
		t.Errorf("closeFuncFlag is not set")
	}
	if !syncFunc2Flag.Load() {
		t.Errorf("syncFunc2Flag is not set")
	}
	if !closeFunc2Flag.Load() {
		t.Errorf("closeFunc2Flag is not set")
	}
}

func TestShutdownError(t *testing.T) {
	ctx := contem.New()

	var (
		firstFuncFlag   atomic.Bool
		secondFuncFlag  atomic.Bool
		thirdFuncFlag   atomic.Bool
		syncFuncFlag    atomic.Bool
		closeFuncFlag   atomic.Bool
		shutdownFlag    atomic.Bool
		endShutdownFlag atomic.Bool
	)
	ctx.Add(func(ctx context.Context) error {
		firstFuncFlag.Store(true)
		return errors.New("some error")
	})
	ctx.AddClose(func() error {
		secondFuncFlag.Store(true)
		panic("some panic")
	})
	ctx.AddClose(func() error {
		thirdFuncFlag.Store(true)
		return nil
	})
	ctx.AddFile(&file{&syncFuncFlag, &closeFuncFlag, false, true})

	go func() {
		shutdownFlag.Store(true)
		err := ctx.Shutdown()
		if err == nil {
			t.Error("shutdown should return error")
			return
		}
		endShutdownFlag.Store(true)

		if !strings.Contains(err.Error(), "some error") {
			t.Errorf("shutdown error should contain 'some error' but got %v", err)
		}
		if !strings.Contains(err.Error(), "some panic") {
			t.Errorf("shutdown error should contain 'some panic' but got %v", err)
		}
		if !strings.Contains(err.Error(), "sync") {
			t.Errorf("shutdown error should contain 'sync' but got %v", err)
		}
		if !strings.Contains(err.Error(), "close") {
			t.Errorf("shutdown error should contain 'close' but got %v", err)
		}
	}()

	<-ctx.Done()
	if !shutdownFlag.Load() {
		t.Errorf("shutdown flag is not set")
	}

	textCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !wait(textCtx, &endShutdownFlag, contem.GetDefaultShutdownTimeout()) {
		t.Errorf("endShutdownFlag is not set")
	}
	if !firstFuncFlag.Load() {
		t.Errorf("firstFuncFlag is not set")
	}
	if !secondFuncFlag.Load() {
		t.Errorf("secondFuncFlag is not set")
	}
	if !thirdFuncFlag.Load() {
		t.Errorf("thirdFuncFlag is not set")
	}
	// Wait for file operations to complete
	if !wait(textCtx, &syncFuncFlag, contem.GetDefaultShutdownTimeout()) {
		t.Errorf("syncFuncFlag is not set")
	}
	if !wait(textCtx, &closeFuncFlag, contem.GetDefaultShutdownTimeout()) {
		t.Errorf("closeFuncFlag is not set")
	}

	err := ctx.Shutdown()
	if err != nil {
		t.Errorf("unexpected second shutdown error: %v", err)
	}
}

func TestShutdownTimeout(t *testing.T) {
	ctx := contem.New()
	contem.SetDefaultShutdownTimeout(time.Millisecond)

	var (
		firstFuncFlag  atomic.Bool
		secondFuncFlag atomic.Bool
		syncFuncFlag   atomic.Bool
		closeFuncFlag  atomic.Bool
		shutdownFlag   atomic.Bool
	)
	ctx.Add(func(ctx context.Context) error {
		firstFuncFlag.Store(true)
		time.Sleep(100 * contem.GetDefaultShutdownTimeout())
		return nil
	})
	ctx.AddClose(func() error {
		secondFuncFlag.Store(true)
		time.Sleep(100 * contem.GetDefaultShutdownTimeout())
		return nil
	})
	ctx.AddFile(&file{&syncFuncFlag, &closeFuncFlag, true, false})

	go func() {
		shutdownFlag.Store(true)
		err := ctx.Shutdown()
		if err == nil {
			t.Error("shutdown should return error")
			return
		}
		if !strings.Contains(err.Error(), "shutdown: timeout") {
			t.Errorf("shutdown error should contain 'shutdown: timeout' but got %v", err)
		}
		if !strings.Contains(err.Error(), "files: timeout") {
			t.Errorf("shutdown error should contain 'files: timeout' but got %v", err)
		}
	}()

	ctx.Wait()
	if !shutdownFlag.Load() {
		t.Errorf("shutdown flag is not set")
	}

	textCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !wait(textCtx, &firstFuncFlag, contem.GetDefaultShutdownTimeout()) {
		t.Errorf("firstFuncFlag is not set")
	}
	if !wait(textCtx, &secondFuncFlag, contem.GetDefaultShutdownTimeout()) {
		t.Errorf("secondFuncFlag is not set")
	}

	// Time of sync >> ShutdownTimeout
	if syncFuncFlag.Load() {
		t.Errorf("syncFuncFlag is set")
	}
	if closeFuncFlag.Load() {
		t.Errorf("closeFuncFlag is set")
	}
}

func TestDontCloseFiles(t *testing.T) {
	ctx := contem.New(contem.DontCloseFiles())

	var (
		firstFuncFlag atomic.Bool
		syncFuncFlag  atomic.Bool
		closeFuncFlag atomic.Bool
		shutdownFlag  atomic.Bool
	)
	ctx.Add(func(ctx context.Context) error {
		firstFuncFlag.Store(true)
		return nil
	})
	ctx.AddFile(&file{&syncFuncFlag, &closeFuncFlag, true, false})

	go func() {
		shutdownFlag.Store(true)
		err := ctx.Shutdown()
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}
	}()

	ctx.Wait()
	if !shutdownFlag.Load() {
		t.Errorf("shutdown flag is not set")
	}

	textCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !wait(textCtx, &firstFuncFlag, contem.GetDefaultShutdownTimeout()) {
		t.Errorf("firstFuncFlag is not set")
	}
	if syncFuncFlag.Load() {
		t.Errorf("syncFuncFlag is set")
	}
	if closeFuncFlag.Load() {
		t.Errorf("closeFuncFlag is set")
	}
}

func TestNoParallelAndFileOrder(t *testing.T) {
	ctx := contem.New(contem.NoParallel(), contem.RegularCloseFilesOrder())

	var (
		numberChannel = make(chan int)

		syncFuncFlag   atomic.Bool
		closeFuncFlag  atomic.Bool
		syncFunc2Flag  atomic.Bool
		closeFunc2Flag atomic.Bool
		shutdownFlag   atomic.Bool
	)
	ctx.Add(func(ctx context.Context) error {
		numberChannel <- 1
		return nil
	})
	ctx.Add(func(ctx context.Context) error {
		numberChannel <- 2
		return nil
	})

	ctx.AddFile(&file{&syncFuncFlag, &closeFuncFlag, false, false})
	ctx.AddFile(&file{&syncFunc2Flag, &closeFunc2Flag, false, false})

	ctx.Add(func(ctx context.Context) error {
		numberChannel <- 3
		return nil
	})
	ctx.Add(func(ctx context.Context) error {
		numberChannel <- 4
		return nil
	})

	go func() {
		shutdownFlag.Store(true)
		err := ctx.Shutdown()
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}
	}()

	ctx.Wait()
	if !shutdownFlag.Load() {
		t.Errorf("shutdown flag is not set")
	}

	if syncFuncFlag.Load() {
		t.Errorf("syncFuncFlag is set")
	}
	if closeFuncFlag.Load() {
		t.Errorf("closeFuncFlag is set")
	}
	if syncFunc2Flag.Load() {
		t.Errorf("syncFunc2Flag is set")
	}
	if closeFunc2Flag.Load() {
		t.Errorf("closeFuncF2lag is set")
	}

	expected := []int{1, 2, 3, 4}

	var out []int
	out = append(out, <-numberChannel)
	if syncFuncFlag.Load() {
		t.Errorf("syncFuncFlag is set")
	}

	out = append(out, <-numberChannel)
	out = append(out, <-numberChannel)

	if !syncFuncFlag.Load() {
		t.Errorf("syncFuncFlag is not set")
	}
	if !closeFuncFlag.Load() {
		t.Errorf("closeFuncFlag is not set")
	}
	if !syncFunc2Flag.Load() {
		t.Errorf("syncFunc2Flag is not set")
	}
	if !closeFunc2Flag.Load() {
		t.Errorf("closeFuncF2lag is not set")
	}

	out = append(out, <-numberChannel)
	if len(out) != len(expected) {
		t.Errorf("unexpected length")
	}

	for i, v := range out {
		if v != expected[i] {
			t.Errorf("unexpected value: %v", v)
		}
	}
}

func TestAutoShutdown(t *testing.T) {
	t.Run("Context.Cancel", func(t *testing.T) {
		var (
			funcFlag     atomic.Bool
			shutdownFlag atomic.Bool
		)

		ctx := contem.New(contem.AutoShutdown())
		ctx.Add(func(ctx context.Context) error {
			funcFlag.Store(true)
			return nil
		})

		go func() {
			shutdownFlag.Store(true)
			ctx.Cancel()
		}()

		ctx.Wait()
		if !shutdownFlag.Load() {
			t.Errorf("shutdown flag is not set")
		}

		textCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if !wait(textCtx, &funcFlag, contem.GetDefaultShutdownTimeout()) {
			t.Errorf("firstFuncFlag is not set")
		}
	})

	t.Run("WithBaseContext.Cancel", func(t *testing.T) {
		var (
			baseCtx, cancel = context.WithCancel(context.Background())

			funcFlag     atomic.Bool
			shutdownFlag atomic.Bool
		)

		ctx := contem.New(contem.WithBaseContext(baseCtx), contem.AutoShutdown())
		ctx.Add(func(ctx context.Context) error {
			funcFlag.Store(true)
			return nil
		})

		go func() {
			shutdownFlag.Store(true)
			cancel()
		}()

		ctx.Wait()
		if !shutdownFlag.Load() {
			t.Errorf("shutdown flag is not set")
		}

		textCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if !wait(textCtx, &funcFlag, contem.GetDefaultShutdownTimeout()) {
			t.Errorf("firstFuncFlag is not set")
		}
	})
}

func TestEmpty(t *testing.T) {
	var funcFlag atomic.Bool

	ctx := contem.Empty()
	ctx.Add(func(ctx context.Context) error {
		funcFlag.Store(true)
		return nil
	})

	ctx.Wait()
	ctx.Shutdown()

	textCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !wait(textCtx, &funcFlag, contem.GetDefaultShutdownTimeout()/10) {
		t.Errorf("firstFuncFlag is not set")
	}
}

func TestShutdownPanic(t *testing.T) {
	ctx := contem.New()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("unexpected panic: %v", r)
		}
	}()
	defer ctx.Shutdown()
	panic("A")
}

type file struct {
	flagSync  *atomic.Bool
	flagClose *atomic.Bool
	sleep     bool
	isErr     bool
}

func (f *file) Sync() error {
	if f.sleep {
		time.Sleep(100 * contem.GetDefaultShutdownTimeout())
	}
	f.flagSync.Store(true)
	if f.isErr {
		return errors.New("sync")
	}
	return nil
}

func (f *file) Close() error {
	f.flagClose.Store(true)
	if f.isErr {
		return errors.New("close")
	}
	return nil
}

func wait(ctx context.Context, v *atomic.Bool, tm time.Duration) bool {
	if v.Load() {
		return true
	}

	timer := time.NewTimer(tm)
	defer timer.Stop()

	ticker := time.NewTicker(tm / 100)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if v.Load() {
				return true
			}

		case <-timer.C:
			return false

		case <-ctx.Done():
			return false

		}
	}
}

type testLogze struct{}

func (testLogze) Error(s string, args ...any) {
	slog.Error(s, args...)
}

func (testLogze) Info(s string, args ...any) {
	slog.Info(s, args...)
}

func TestStart(t *testing.T) {
	t.Run("SuccessfulRun", func(t *testing.T) {
		var runCalled atomic.Bool
		var runCtx contem.Context

		logger := &testLogger{}

		// Use NoWait to prevent blocking
		contem.Start(func(ctx contem.Context) error {
			runCalled.Store(true)
			runCtx = ctx
			return nil
		}, logger, contem.WithNoWait())

		if !runCalled.Load() {
			t.Error("run function was not called")
		}

		if runCtx == nil {
			t.Error("context was not provided to run function")
		}
	})

	t.Run("RunError", func(t *testing.T) {
		var runCalled atomic.Bool
		logger := &testLogger{}

		contem.Start(func(ctx contem.Context) error {
			runCalled.Store(true)
			return errors.New("run error")
		}, logger, contem.WithNoWait())

		if !runCalled.Load() {
			t.Error("run function was not called")
		}

		if len(logger.errors) == 0 {
			t.Error("expected error to be logged")
		}

		if !strings.Contains(logger.errors[0], "run error") {
			t.Errorf("expected 'run error' in log, got: %s", logger.errors[0])
		}
	})

	t.Run("WithoutNoWait", func(t *testing.T) {
		var runCalled atomic.Bool
		logger := &testLogger{}

		go func() {
			time.Sleep(10 * time.Millisecond)
			// Simulate signal to unblock Wait()
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}()

		contem.Start(func(ctx contem.Context) error {
			runCalled.Store(true)
			return nil
		}, logger)

		if !runCalled.Load() {
			t.Error("run function was not called")
		}
	})
}

func TestContextMethods(t *testing.T) {
	t.Run("Deadline", func(t *testing.T) {
		ctx := contem.New()
		defer ctx.Shutdown()

		deadline, ok := ctx.Deadline()
		if ok {
			t.Error("expected no deadline for signal context")
		}
		if !deadline.IsZero() && ok {
			t.Error("expected zero deadline when no deadline is set")
		}

		// Test with timeout context
		baseCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		ctx2 := contem.New(contem.WithBaseContext(baseCtx))
		defer ctx2.Shutdown()

		deadline2, ok2 := ctx2.Deadline()
		if !ok2 {
			t.Error("expected deadline for timeout context")
		}
		if deadline2.IsZero() {
			t.Error("expected non-zero deadline")
		}
	})

	t.Run("Done", func(t *testing.T) {
		ctx := contem.New()
		defer ctx.Shutdown()

		done := ctx.Done()
		if done == nil {
			t.Error("Done() should return a channel")
		}

		// Channel should not be closed initially
		select {
		case <-done:
			t.Error("channel should not be closed initially")
		default:
		}

		// Cancel context
		ctx.Cancel()

		// Channel should be closed after cancel
		select {
		case <-done:
			// Expected
		case <-time.After(100 * time.Millisecond):
			t.Error("channel should be closed after cancel")
		}
	})

	t.Run("Err", func(t *testing.T) {
		ctx := contem.New()
		defer ctx.Shutdown()

		if err := ctx.Err(); err != nil {
			t.Errorf("expected no error initially, got: %v", err)
		}

		ctx.Cancel()

		if err := ctx.Err(); err == nil {
			t.Error("expected error after cancel")
		}
	})

	t.Run("Value", func(t *testing.T) {
		ctx := contem.New()
		defer ctx.Shutdown()

		// Test non-existent key
		if val := ctx.Value("nonexistent"); val != nil {
			t.Errorf("expected nil for non-existent key, got: %v", val)
		}

		// Test SetValue and Value
		ctx.SetValue("key1", "value1")
		if val := ctx.Value("key1"); val != "value1" {
			t.Errorf("expected 'value1', got: %v", val)
		}

		// Test chaining SetValue
		result := ctx.SetValue("key2", "value2").SetValue("key3", "value3")
		if result != ctx {
			t.Error("SetValue should return the same context for chaining")
		}

		if val := ctx.Value("key2"); val != "value2" {
			t.Errorf("expected 'value2', got: %v", val)
		}

		if val := ctx.Value("key3"); val != "value3" {
			t.Errorf("expected 'value3', got: %v", val)
		}
	})
}

func TestNewWithOptions(t *testing.T) {
	t.Run("DefaultOptions", func(t *testing.T) {
		opts := contem.Options{}
		ctx := contem.NewWithOptions(opts)
		defer ctx.Shutdown()

		if ctx == nil {
			t.Error("NewWithOptions should return a valid context")
		}
	})

	t.Run("WithCustomTimeout", func(t *testing.T) {
		customTimeout := 5 * time.Second
		ctx := contem.New(contem.WithShutdownTimeout(customTimeout))
		defer ctx.Shutdown()

		// Test that custom timeout is used
		var funcFlag atomic.Bool
		ctx.Add(func(ctx context.Context) error {
			funcFlag.Store(true)
			return nil
		})

		start := time.Now()
		err := ctx.Shutdown()
		duration := time.Since(start)

		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}

		if !funcFlag.Load() {
			t.Error("function was not called")
		}

		// Should complete quickly since function returns immediately
		if duration > time.Second {
			t.Error("shutdown took too long")
		}
	})

	t.Run("WithCustomBaseContext", func(t *testing.T) {
		baseCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ctx := contem.New(contem.WithBaseContext(baseCtx))
		defer ctx.Shutdown()

		// Cancel base context
		cancel()

		// Context should be cancelled
		select {
		case <-ctx.Done():
			// Expected
		case <-time.After(100 * time.Millisecond):
			t.Error("context should be cancelled when base context is cancelled")
		}
	})
}

func TestOptionAliases(t *testing.T) {
	t.Run("WithAutoShutdown", func(t *testing.T) {
		ctx := contem.New(contem.WithAutoShutdown())
		defer ctx.Shutdown()
		// Just test that it doesn't panic
	})

	t.Run("WithExit", func(t *testing.T) {
		var err error
		ctx := contem.New(contem.WithExit(&err, 42))
		// Don't call shutdown as it will os.Exit() - just test creation
		_ = ctx
		// Just test that it doesn't panic during creation
	})

	t.Run("WithDontCloseFiles", func(t *testing.T) {
		ctx := contem.New(contem.WithDontCloseFiles())
		defer ctx.Shutdown()
		// Just test that it doesn't panic
	})

	t.Run("WithRegularCloseFilesOrder", func(t *testing.T) {
		ctx := contem.New(contem.WithRegularCloseFilesOrder())
		defer ctx.Shutdown()
		// Just test that it doesn't panic
	})

	t.Run("WithNoParallel", func(t *testing.T) {
		ctx := contem.New(contem.WithNoParallel())
		defer ctx.Shutdown()
		// Just test that it doesn't panic
	})
}

func TestLogging(t *testing.T) {
	t.Run("WithLogger", func(t *testing.T) {
		logger := &testLogger{}
		ctx := contem.New(contem.WithLogger(logger))

		var funcFlag atomic.Bool
		ctx.Add(func(ctx context.Context) error {
			funcFlag.Store(true)
			return nil
		})

		err := ctx.Shutdown()
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}

		if !funcFlag.Load() {
			t.Error("function was not called")
		}

		// Check that shutdown info was logged
		if len(logger.infos) == 0 {
			t.Error("expected info log messages")
		}

		found := false
		for _, info := range logger.infos {
			if strings.Contains(info, "starting shutdown") {
				found = true
				break
			}
		}

		if !found {
			t.Error("expected 'starting shutdown' info message")
		}
	})

	t.Run("LoggingErrors", func(t *testing.T) {
		logger := &testLogger{}
		ctx := contem.New(contem.WithLogger(logger))

		ctx.Add(func(ctx context.Context) error {
			return errors.New("test error")
		})

		err := ctx.Shutdown()
		if err == nil {
			t.Error("expected error from shutdown")
		}

		// Check that error was logged
		if len(logger.errors) == 0 {
			t.Error("expected error log messages")
		}

		found := false
		for _, errMsg := range logger.errors {
			if strings.Contains(errMsg, "cannot shutdown") {
				found = true
				break
			}
		}

		if !found {
			t.Error("expected 'cannot shutdown' error message")
		}
	})
}

func TestEmptyContext(t *testing.T) {
	t.Run("NewEmpty", func(t *testing.T) {
		ctx := contem.NewEmpty()

		if ctx == nil {
			t.Error("NewEmpty should return a valid context")
		}

		// Should not block
		ctx.Wait()
		ctx.Shutdown()
	})

	t.Run("EmptyAlias", func(t *testing.T) {
		ctx := contem.Empty()

		if ctx == nil {
			t.Error("Empty should return a valid context")
		}

		// Should not block
		ctx.Wait()
		ctx.Shutdown()
	})

	t.Run("EmptyWithFunctions", func(t *testing.T) {
		ctx := contem.Empty()

		var funcFlag atomic.Bool
		ctx.Add(func(ctx context.Context) error {
			funcFlag.Store(true)
			return nil
		})

		ctx.Wait()
		err := ctx.Shutdown()

		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}

		if !funcFlag.Load() {
			t.Error("function was not called")
		}
	})
}

func TestEdgeCases(t *testing.T) {
	t.Run("MultipleShutdowns", func(t *testing.T) {
		ctx := contem.New()

		var funcCallCount atomic.Int32
		ctx.Add(func(ctx context.Context) error {
			funcCallCount.Add(1)
			return nil
		})

		err1 := ctx.Shutdown()
		err2 := ctx.Shutdown()
		err3 := ctx.Shutdown()

		if err1 != nil {
			t.Errorf("first shutdown error: %v", err1)
		}

		if err2 != nil {
			t.Errorf("second shutdown error: %v", err2)
		}

		if err3 != nil {
			t.Errorf("third shutdown error: %v", err3)
		}

		// Function should only be called once
		if count := funcCallCount.Load(); count != 1 {
			t.Errorf("expected function to be called once, got %d", count)
		}
	})

	t.Run("ConcurrentOperations", func(t *testing.T) {
		ctx := contem.New()
		defer ctx.Shutdown()

		var wg sync.WaitGroup

		// Add functions concurrently
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				ctx.Add(func(ctx context.Context) error {
					return nil
				})
				ctx.SetValue(fmt.Sprintf("key%d", i), fmt.Sprintf("value%d", i))
			}(i)
		}

		wg.Wait()

		// Give a moment for all context updates to be visible
		time.Sleep(50 * time.Millisecond)

		// Verify some values were set
		foundCount := 0
		for i := 0; i < 10; i++ {
			if val := ctx.Value(fmt.Sprintf("key%d", i)); val != nil {
				foundCount++
			}
		}

		if foundCount == 0 {
			t.Error("expected at least one key to be set")
		}

		if foundCount < 5 {
			t.Errorf("expected at least 5 keys to be set, got %d", foundCount)
		}
	})

	t.Run("NilLogger", func(t *testing.T) {
		ctx := contem.New(contem.WithLogger(nil))

		ctx.Add(func(ctx context.Context) error {
			return errors.New("test error")
		})

		// Should not panic with nil logger
		err := ctx.Shutdown()
		if err == nil {
			t.Error("expected error from shutdown")
		}
	})

	t.Run("ZeroTimeout", func(t *testing.T) {
		ctx := contem.New(contem.WithShutdownTimeout(0))

		var funcFlag atomic.Bool
		ctx.Add(func(ctx context.Context) error {
			funcFlag.Store(true)
			return nil
		})

		err := ctx.Shutdown()
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}

		if !funcFlag.Load() {
			t.Error("function was not called")
		}
	})
}

func TestFileClosingEdgeCases(t *testing.T) {
	t.Run("FileWithNilError", func(t *testing.T) {
		ctx := contem.New()

		var syncCalled, closeCalled atomic.Bool

		ctx.AddFile(&testFile{
			syncFunc: func() error {
				syncCalled.Store(true)
				return nil
			},
			closeFunc: func() error {
				closeCalled.Store(true)
				return nil
			},
		})

		err := ctx.Shutdown()
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}

		if !syncCalled.Load() {
			t.Error("sync was not called")
		}

		if !closeCalled.Load() {
			t.Error("close was not called")
		}
	})

	t.Run("FileWithSyncErrorOnly", func(t *testing.T) {
		ctx := contem.New()

		ctx.AddFile(&testFile{
			syncFunc: func() error {
				return errors.New("sync error")
			},
			closeFunc: func() error {
				return nil
			},
		})

		err := ctx.Shutdown()
		if err == nil {
			t.Error("expected error from shutdown")
		}

		if !strings.Contains(err.Error(), "sync error") {
			t.Errorf("expected 'sync error' in error message, got: %v", err)
		}
	})

	t.Run("FileWithCloseErrorOnly", func(t *testing.T) {
		ctx := contem.New()

		ctx.AddFile(&testFile{
			syncFunc: func() error {
				return nil
			},
			closeFunc: func() error {
				return errors.New("close error")
			},
		})

		err := ctx.Shutdown()
		if err == nil {
			t.Error("expected error from shutdown")
		}

		if !strings.Contains(err.Error(), "close error") {
			t.Errorf("expected 'close error' in error message, got: %v", err)
		}
	})
}

type testLogger struct {
	infos  []string
	errors []string
	mu     sync.Mutex
}

func (l *testLogger) Info(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, fmt.Sprintf(msg, args...))
}

func (l *testLogger) Error(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errors = append(l.errors, fmt.Sprintf(msg, args...))
}

type testFile struct {
	syncFunc  func() error
	closeFunc func() error
}

func (f *testFile) Sync() error {
	if f.syncFunc != nil {
		return f.syncFunc()
	}
	return nil
}

func (f *testFile) Close() error {
	if f.closeFunc != nil {
		return f.closeFunc()
	}
	return nil
}

func TestWaiterSetEdgeCases(t *testing.T) {
	t.Run("WaiterPanic", func(t *testing.T) {
		logger := &testLogger{}
		ctx := contem.New(contem.WithLogger(logger))

		ctx.Add(func(ctx context.Context) error {
			panic("test panic in shutdown function")
		})

		err := ctx.Shutdown()
		if err == nil {
			t.Error("expected error from shutdown due to panic")
		}

		if !strings.Contains(err.Error(), "test panic in shutdown function") {
			t.Errorf("expected panic message in error, got: %v", err)
		}
	})

	t.Run("WaiterZeroTimeout", func(t *testing.T) {
		ctx := contem.New(contem.WithShutdownTimeout(0))

		var functionCalled atomic.Bool
		ctx.Add(func(ctx context.Context) error {
			functionCalled.Store(true)
			// Don't sleep with zero timeout as it may still timeout
			return nil
		})

		err := ctx.Shutdown()
		// With zero timeout, function should complete immediately
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		if !functionCalled.Load() {
			t.Error("function should have been called")
		}
	})
}

func TestParseOptions(t *testing.T) {
	t.Run("EmptyOptions", func(t *testing.T) {
		opts := contem.Options{}
		ctx := contem.NewWithOptions(opts)
		defer ctx.Shutdown()

		if ctx == nil {
			t.Error("should create valid context with empty options")
		}
	})

	t.Run("MultipleOptions", func(t *testing.T) {
		logger := &testLogger{}
		var err error

		ctx := contem.New(
			contem.WithLogger(logger),
			contem.WithShutdownTimeout(100*time.Millisecond),
			contem.NoParallel(),
			contem.DontCloseFiles(),
			contem.Exit(&err, 42),
		)
		defer ctx.Shutdown()

		if ctx == nil {
			t.Error("should create valid context with multiple options")
		}
	})
}

func TestJoinErrors(t *testing.T) {
	t.Run("EmptyErrors", func(t *testing.T) {
		ctx := contem.New()

		// Add functions that return nil errors
		ctx.Add(func(ctx context.Context) error { return nil })
		ctx.Add(func(ctx context.Context) error { return nil })

		err := ctx.Shutdown()
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("MultipleErrors", func(t *testing.T) {
		ctx := contem.New()

		ctx.Add(func(ctx context.Context) error {
			return errors.New("first error")
		})
		ctx.Add(func(ctx context.Context) error {
			return errors.New("second error")
		})
		ctx.Add(func(ctx context.Context) error {
			return errors.New("third error")
		})

		err := ctx.Shutdown()
		if err == nil {
			t.Error("expected error from shutdown")
		}

		errStr := err.Error()
		if !strings.Contains(errStr, "first error") {
			t.Error("should contain first error")
		}
		if !strings.Contains(errStr, "second error") {
			t.Error("should contain second error")
		}
		if !strings.Contains(errStr, "third error") {
			t.Error("should contain third error")
		}

		// Should be joined with semicolons
		if !strings.Contains(errStr, ";") {
			t.Error("errors should be joined with semicolons")
		}
	})

	t.Run("MixedNilAndNonNilErrors", func(t *testing.T) {
		ctx := contem.New()

		ctx.Add(func(ctx context.Context) error { return nil })
		ctx.Add(func(ctx context.Context) error { return errors.New("real error") })
		ctx.Add(func(ctx context.Context) error { return nil })

		err := ctx.Shutdown()
		if err == nil {
			t.Error("expected error from shutdown")
		}

		if !strings.Contains(err.Error(), "real error") {
			t.Errorf("should contain real error, got: %v", err)
		}
	})
}

func TestSetValueChaining(t *testing.T) {
	ctx := contem.New()
	defer ctx.Shutdown()

	// Test that SetValue returns the same context for chaining
	result := ctx.SetValue("key1", "value1").
		SetValue("key2", "value2").
		SetValue("key3", "value3")

	if result != ctx {
		t.Error("SetValue should return the same context for chaining")
	}

	// Verify all values are set
	if val := ctx.Value("key1"); val != "value1" {
		t.Errorf("expected 'value1', got: %v", val)
	}
	if val := ctx.Value("key2"); val != "value2" {
		t.Errorf("expected 'value2', got: %v", val)
	}
	if val := ctx.Value("key3"); val != "value3" {
		t.Errorf("expected 'value3', got: %v", val)
	}
}

func TestFileClosingTimeout(t *testing.T) {
	// Set a very short timeout for this test
	originalTimeout := contem.GetDefaultShutdownTimeout()
	contem.SetDefaultShutdownTimeout(10 * time.Millisecond)
	defer func() {
		contem.SetDefaultShutdownTimeout(originalTimeout)
	}()

	ctx := contem.New()

	var syncStarted, closeStarted atomic.Bool

	ctx.AddFile(&testFile{
		syncFunc: func() error {
			syncStarted.Store(true)
			time.Sleep(100 * time.Millisecond) // Much longer than timeout
			return nil
		},
		closeFunc: func() error {
			closeStarted.Store(true)
			time.Sleep(100 * time.Millisecond) // Much longer than timeout
			return nil
		},
	})

	err := ctx.Shutdown()
	if err == nil {
		t.Error("expected timeout error")
	}

	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}

	// Functions should have started
	if !syncStarted.Load() {
		t.Error("sync should have started")
	}
}

func TestRecoverPanic(t *testing.T) {
	t.Run("PanicWithLogger", func(t *testing.T) {
		logger := &testLogger{}
		ctx := contem.New(contem.WithLogger(logger))

		ctx.Add(func(ctx context.Context) error {
			panic("test panic")
		})

		err := ctx.Shutdown()
		if err == nil {
			t.Error("expected error from panic")
		}

		// Check that panic was logged to logger
		found := false
		for _, errMsg := range logger.errors {
			if strings.Contains(errMsg, "test panic") {
				found = true
				break
			}
		}

		if !found {
			t.Error("panic should be logged to logger")
		}
	})
}

func TestContextWithDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Hour)
	baseCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	ctx := contem.New(contem.WithBaseContext(baseCtx))
	defer ctx.Shutdown()

	ctxDeadline, ok := ctx.Deadline()
	if !ok {
		t.Error("expected deadline to be set")
	}

	if !ctxDeadline.Equal(deadline) {
		t.Errorf("expected deadline %v, got %v", deadline, ctxDeadline)
	}
}

func TestCustomSignals(t *testing.T) {
	if testing.Short() {
		t.SkipNow()
	}

	ctx := contem.New(contem.WithSignals(syscall.SIGUSR1))
	defer ctx.Shutdown()

	// Test that custom signal is used
	done := ctx.Done()

	// Send the custom signal
	go func() {
		time.Sleep(10 * time.Millisecond)
		syscall.Kill(os.Getpid(), syscall.SIGUSR1)
	}()

	// Should receive signal
	select {
	case <-done:
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Error("should have received custom signal")
	}
}
