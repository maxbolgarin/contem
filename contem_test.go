package contem_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
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
		firstFuncFlag   atomic.Bool
		secondFuncFlag  atomic.Bool
		thirdFuncFlag   atomic.Bool
		syncFuncFlag    atomic.Bool
		closeFuncFlag   atomic.Bool
		syncFunc2Flag   atomic.Bool
		closeFunc2Flag  atomic.Bool
		shutdownFlag    atomic.Bool
		endShutdownFlag atomic.Bool
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

	go func() {
		shutdownFlag.Store(true)
		err := ctx.Shutdown()
		if err != nil {
			t.Errorf("shutdown error: %v", err)
		}
		endShutdownFlag.Store(true)
	}()

	ctx.Wait()
	if !shutdownFlag.Load() {
		t.Errorf("shutdown flag is not set")
	}

	textCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !wait(textCtx, &endShutdownFlag, contem.ShutdownTimeout) {
		t.Errorf("endShutdownFlag is not set")
	}
	if !firstFuncFlag.Load() {
		t.Errorf("secondFuncFlag is not set")
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
	ctx := contem.New(contem.WithLogze(testLogze{}))

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

	if !wait(textCtx, &endShutdownFlag, contem.ShutdownTimeout) {
		t.Errorf("endShutdownFlag is not set")
	}
	if !firstFuncFlag.Load() {
		t.Errorf("secondFuncFlag is not set")
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

	err := ctx.Shutdown()
	if err != nil {
		t.Errorf("unexpected second shutdown error: %v", err)
	}
}

func TestShutdownTimeout(t *testing.T) {
	ctx := contem.New()
	contem.ShutdownTimeout = time.Millisecond

	var (
		firstFuncFlag  atomic.Bool
		secondFuncFlag atomic.Bool
		syncFuncFlag   atomic.Bool
		closeFuncFlag  atomic.Bool
		shutdownFlag   atomic.Bool
	)
	ctx.Add(func(ctx context.Context) error {
		firstFuncFlag.Store(true)
		time.Sleep(100 * contem.ShutdownTimeout)
		return nil
	})
	ctx.AddClose(func() error {
		secondFuncFlag.Store(true)
		time.Sleep(100 * contem.ShutdownTimeout)
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

	if !wait(textCtx, &firstFuncFlag, contem.ShutdownTimeout) {
		t.Errorf("firstFuncFlag is not set")
	}
	if !wait(textCtx, &secondFuncFlag, contem.ShutdownTimeout) {
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

	if !wait(textCtx, &firstFuncFlag, contem.ShutdownTimeout) {
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

		if !wait(textCtx, &funcFlag, contem.ShutdownTimeout) {
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

		if !wait(textCtx, &funcFlag, contem.ShutdownTimeout) {
			t.Errorf("firstFuncFlag is not set")
		}
	})
}

func TestSignals(t *testing.T) {
	// This isn't working in github actions
	if testing.Short() {
		t.SkipNow()
	}

	var funcFlag atomic.Bool

	ctx := contem.New(contem.WithSignals(syscall.SIGALRM), contem.AutoShutdown())
	ctx.Add(func(ctx context.Context) error {
		funcFlag.Store(true)
		return nil
	})
	syscall.Kill(os.Getpid(), syscall.SIGALRM)

	ctx.Wait()

	textCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if !wait(textCtx, &funcFlag, contem.ShutdownTimeout/10) {
		t.Errorf("firstFuncFlag is not set")
	}
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

	if !wait(textCtx, &funcFlag, contem.ShutdownTimeout/10) {
		t.Errorf("firstFuncFlag is not set")
	}
}

func TestExit(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			if !strings.Contains(r.(string), "os.Exit(0)") {
				t.Errorf("unexpected panic: %v", r)
			}
		}
	}()
	var err error
	ctx := contem.New(contem.Exit(&err, 123))
	ctx.Add(func(ctx context.Context) error {
		return nil
	})
	ctx.Shutdown()
	t.Error("should exit")
}

type file struct {
	flagSync  *atomic.Bool
	flagClose *atomic.Bool
	sleep     bool
	isErr     bool
}

func (f *file) Sync() error {
	if f.sleep {
		time.Sleep(100 * contem.ShutdownTimeout)
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

func (testLogze) Error(err error, s string, args ...any) {
	slog.Error(s, args...)
}

func (testLogze) Info(s string, args ...any) {
	slog.Info(s, args...)
}
