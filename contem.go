// Package contem provides a context for graceful shutdown an application,
// based on the receiving interruption signal from the OS.
package contem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ShutdownTimeout is a timeout for context in every added Shutdown function.
// You can change it before calling [Context.Shutdown] method to change shutdown timeout,
// it may be useful if you have a lot of shutdown functions or they are slow.
var ShutdownTimeout = 15 * time.Second

// ShutdownFunc represents a shutdown function.
type ShutdownFunc func(ctx context.Context) error

// CloseFunc represents a close function from [io.Closer] interface.
type CloseFunc func() error

// Context is an interface that can be used in function definitions instead of [context.Context].
// You can just replace context.Context with contem.Context and everything will be working the same.
//
// When you should use [Context] instead of [context.Context]?
//  1. You want to shutdown gracefully all services, servers, databases, etc in a one place using a single method.
//  2. You want to shutdown application using ctrl+c command.
//  3. You have a closable resource in the internals of your program, that won't be returned to the caller (e.g. log file).
//     You can add it to  the[Context] and you won't forget to close it.
//     Of course, GC will automatically close all files after returning from main(), but you shouldn't rely on it.
//  4. You want to cancel provided context on the "child" level — this is a bad pattern, but there are some fatal cases
//     like server errors (that runs in a separate goroutine) that should lead to an application graceful shutdown
//     (using os.Exit is worse imo).
type Context interface {
	// Context is just a context wrapper to drop-off replacement of context.Context
	context.Context

	// Add adds a shutdown function to the list of functions that will be called in the [Context.Shutdown] method.
	Add(ShutdownFunc)

	// AddClose adds a close function (from [io.Closer]) to the list of functions
	// that will be called in the [Context.Shutdown] method.
	AddClose(CloseFunc)

	// AddFunc adds a plain function to the list of functions that will be called in the [Context.Shutdown] method.
	AddFunc(f func())

	// AddFile adds a [File] to the list of functions that will be called in [Context.Shutdown] method
	// after all another closing methods (they can produce output to files, for example).
	AddFile(File)

	// SetValue sets a value to the underlying context. You can get this value using [Context.Value] method.
	// It updates original context.
	SetValue(key, value any) Context

	// Wait blocks until the channel is closed (recieving [syscall.SIGINT] and [syscall.SIGTERM] signals by default).
	// It should be used in main() function after an application start to wait for a interruption.
	Wait()

	// Cancel cancels an underlying context. Using this method is a bad practice, because it allows you to start
	// [Context.Shutdown] from any place in your code, not only from main.
	// It can be useful in some cases (e.g. handle [http.ListenAndServe] error), but it's not recommended.
	Cancel()

	// Shutdown cancels an underlying context, then calls every added function with [ShutdownTimeout] in parallel.
	// It will return an error if timeout exceeds or if any of shutdown functions returns error.
	Shutdown() error
}

// File is an interface with operation that need to be called during file closing.
type File interface {
	Sync() error
	Close() error
}

var _ context.Context = (*contextImpl)(nil)
var _ Context = (*contextImpl)(nil)

type contextImpl struct {
	funcs       []ShutdownFunc
	fileClosers []CloseFunc

	ctx    context.Context
	cancel func()

	log      Logger
	outerErr *error
	exit     bool
	logging  bool
	noFiles  bool

	isClosed atomic.Bool
	mu       sync.Mutex
}

// New returns a ready to use [Context] with a created [signal.NotifyContext]
// listening to [syscall.SIGINT] and [syscall.SIGTERM] signals by default.
// You also can provide your custom signals, custom logger or other options.
func New(opts ...Option) Context {
	op := parseOptions(opts...)

	if op.baseCtx == nil {
		op.baseCtx = context.Background()
	}

	if len(op.signals) == 0 {
		op.signals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	}

	ctx, cancel := signal.NotifyContext(op.baseCtx, op.signals...)

	ct := &contextImpl{
		ctx:      ctx,
		cancel:   cancel,
		log:      op.log,
		outerErr: op.outerErr,
		exit:     op.exit,
		logging:  op.logging,
		noFiles:  op.noFiles,
	}

	if op.auto {
		go func() {
			select {
			case <-ctx.Done():
				_ = ct.Shutdown()
			}
		}()
	}

	return ct
}

// Empty returns a dummy [Context] with [context.Background] context. It is useful for tests.
func Empty() Context {
	return &contextImpl{ctx: context.Background(), cancel: func() {}}
}

// Add adds a shutdown function to the list of functions that will be called in the [Context.Shutdown] method.
func (ct *contextImpl) Add(f ShutdownFunc) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.funcs = append(ct.funcs, f)
}

// AddClose adds a close function (from [io.Closer]) to the list of functions
// that will be called in the [Context.Shutdown] method.
func (ct *contextImpl) AddClose(f CloseFunc) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.funcs = append(ct.funcs, func(context.Context) error {
		return f()
	})
}

// AddFunc adds a plain function to the list of functions that will be called in the [Context.Shutdown] method.
func (ct *contextImpl) AddFunc(f func()) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.funcs = append(ct.funcs, func(context.Context) error {
		f()
		return nil
	})
}

// AddFile adds a [File] to the list of functions that will be called in [Context.Shutdown] method
// after all another closing methods (they can produce output to files, for example).
func (ct *contextImpl) AddFile(f File) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.fileClosers = append(ct.fileClosers, func() error {
		var errs []error
		if err := f.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("sync: %w", err))
		}
		if err := f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close: %w", err))
		}
		return joinErrors(errs)
	})
}

// SetValue sets a value to the underlying context. You can get this value using [Context.Value] method.
// It updates original context.
func (ct *contextImpl) SetValue(key, value any) Context {
	ct.ctx = context.WithValue(ct.ctx, key, value)
	return ct
}

// Wait blocks until the channel is closed (recieving [syscall.SIGINT] and [syscall.SIGTERM] signals by default).
// It should be used in main() function after an application start to wait for a interruption.
func (ct *contextImpl) Wait() {
	if ch := ct.ctx.Done(); ch != nil {
		<-ch
	}
}

// Cancel cancels an underlying context. Using this method is a bad practice, because it allows you to
// [Context.Shutdown] from any place inside your code, not only from main.
// It can be useful in some cases (e.g. handle [http.ListenAndServe] error), but it's not recommended.
func (ct *contextImpl) Cancel() {
	ct.cancel()
}

// Shutdown cancels an underlying context, then calls every added function with timeout in parallel.
// It will return an error if timeout exceeds or any of shutdown functions returns error.
func (ct *contextImpl) Shutdown() error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if ct.isClosed.Load() {
		return nil
	}
	ct.isClosed.Store(true)
	ct.cancel()

	if ct.logging {
		ct.log.Info("starting shutdown")
	}

	var (
		start = time.Now()
		ws    = newWaiterSet(ct.log)
		errs  []error
	)

	shutdownCtx, cancel := context.WithTimeout(ct.ctx, ShutdownTimeout)
	defer cancel()

	for _, f := range ct.funcs {
		ws.add(shutdownCtx, f)
	}

	err := ws.await(start, ShutdownTimeout)
	if err != nil {
		errs = append(errs, fmt.Errorf("shutdown: %w", err))
	}

	if !ct.noFiles {
		closeFilesTimeout := ShutdownTimeout - time.Since(start)
		if err := ct.closeFiles(ws, closeFilesTimeout); err != nil {
			errs = append(errs, fmt.Errorf("files: %w", err))
		}
	}

	serr := joinErrors(errs)
	if serr != nil {
		if ct.logging {
			ct.log.Error("cannot shutdown", "error", serr)
		}
		ct.outerErr = &serr // we will os.Exit(1) in any way
	}

	if ct.exit {
		time.Sleep(100 * time.Millisecond) // wait for flush
		if ct.outerErr != nil && *ct.outerErr != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}

	return serr
}

// Deadline returns the time when work done on behalf of this context should be canceled.
// Deadline returns ok==false when no deadline is set.
func (c *contextImpl) Deadline() (time.Time, bool) {
	return c.ctx.Deadline()
}

// Done returns a channel that will be closed (after receiving [syscall.SIGINT] or [syscall.SIGTERM] signal by default).
func (c *contextImpl) Done() <-chan struct{} {
	return c.ctx.Done()
}

// Err returns nil if Done is not yet closed, if Done is closed, Err returns a non-nil error explaining why.
func (c *contextImpl) Err() error {
	return c.ctx.Err()
}

// Value returns the value associated with this context for key, or nil if no value is associated with key.
func (c *contextImpl) Value(key any) any {
	return c.ctx.Value(key)
}

func (c *contextImpl) closeFiles(ws *waiterSet, timeout time.Duration) error {
	if len(c.fileClosers) == 0 {
		return nil
	}

	if timeout < ShutdownTimeout/5 {
		timeout = ShutdownTimeout / 5
	}

	for _, f := range c.fileClosers {
		ws.add(context.Background(), func(context.Context) error {
			return f()
		})
	}

	err := ws.await(time.Now(), timeout)
	if err != nil {
		return err
	}

	return nil
}

type waiterSet struct {
	ws []*waiter
	l  Logger
}

func newWaiterSet(l Logger) *waiterSet {
	return &waiterSet{l: l}
}

func (s *waiterSet) add(ctx context.Context, foo ShutdownFunc) {
	s.ws = append(s.ws, newWaiter(ctx, foo, s.l))
}

func (s *waiterSet) await(start time.Time, timeout time.Duration) error {
	var errs []error
	for _, w := range s.ws {
		currentTimeout := timeout - time.Since(start)
		if currentTimeout < 0 {
			currentTimeout = 0
		}
		err := w.await(currentTimeout)
		if err != nil {
			errs = append(errs, err)
		}
	}
	s.ws = nil

	return joinErrors(errs)
}

type waiter struct {
	err  error
	done chan struct{}
}

func newWaiter(ctx context.Context, foo ShutdownFunc, l Logger) *waiter {
	w := &waiter{
		done: make(chan struct{}),
	}

	go func() {
		defer close(w.done)
		defer func() {
			if panicErr := recover(); panicErr != nil {
				stack := string(debug.Stack())
				w.err = fmt.Errorf("%+v\n%s", panicErr, stack)
				if l != nil {
					l.Error("panic", "error", panicErr, "stack", stack)
				}
			}
		}()
		w.err = foo(ctx)
	}()

	return w
}

func (f *waiter) await(timeout time.Duration) error {
	// Firstly try to get result without checking the context and timeout.
	select {
	case <-f.done:
		return f.err
	default:
	}

	select {
	case <-time.After(timeout):
		return errors.New("timeout")
	case <-f.done:
		return f.err
	}
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}

	var b []byte
	for i, err := range errs {
		if err == nil {
			continue
		}
		msg := err.Error()
		if msg == "" {
			continue
		}
		if i > 0 {
			b = append(b, ';', ' ')
		}
		b = append(b, msg...)
	}

	if len(b) == 0 {
		return nil
	}

	return errors.New(string(b))
}
