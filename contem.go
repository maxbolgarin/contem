// Package contem provides a context for graceful shutdown of applications,
// based on receiving interruption signals from the OS.
package contem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// DefaultShutdownTimeout is the default timeout for context in every added [ShutdownFunc] function.
// Use [SetDefaultShutdownTimeout] to change the default timeout safely.
const DefaultShutdownTimeout = 15 * time.Second

var defaultShutdownTimeout atomic.Int64

func init() {
	defaultShutdownTimeout.Store(int64(DefaultShutdownTimeout))
}

// SetDefaultShutdownTimeout sets the default shutdown timeout safely.
// This affects new contexts created without an explicit timeout.
func SetDefaultShutdownTimeout(timeout time.Duration) {
	defaultShutdownTimeout.Store(int64(timeout))
}

// GetDefaultShutdownTimeout returns the current default shutdown timeout.
func GetDefaultShutdownTimeout() time.Duration {
	return time.Duration(defaultShutdownTimeout.Load())
}

// ShutdownFunc represents a shutdown function that accepts a context and returns an error.
type ShutdownFunc func(ctx context.Context) error

// CloseFunc represents a close function from the [io.Closer] interface.
type CloseFunc func() error

// Context is an interface that can be used in function definitions instead of [context.Context].
// You can just replace context.Context with contem.Context and everything will work the same.
//
// When should you use [Context] instead of [context.Context]?
//  1. You want to shutdown gracefully all services, servers, databases, etc in one place using a single method.
//  2. You want to shutdown the application using ctrl+c command.
//  3. You have a closable resource in the internals of your program that won't be returned to the caller (e.g. log file).
//     You can add it to the [Context] and you won't forget to close it.
//     Of course, GC will automatically close all files after returning from main(), but you shouldn't rely on it.
//  4. You want to cancel the provided context on the "child" level — this is a bad pattern, but there are some fatal cases
//     like server errors (that run in separate goroutines) that should lead to an application graceful shutdown
//     (using [os.Exit] is worse in my opinion).
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

	// AddFile adds a [File] to the list of functions that will be called in the [Context.Shutdown] method
	// after all other closing methods (they can produce output to files, for example).
	AddFile(File)

	// SetValue sets a value to the underlying context. You can get this value using the [Context.Value] method.
	// It updates the original context.
	SetValue(key, value any) Context

	// Wait blocks until the channel is closed (receiving [syscall.SIGINT] and [syscall.SIGTERM] signals by default).
	// It should be used in the main() function after application start to wait for an interruption.
	Wait()

	// Cancel cancels an underlying context. Using this method is a bad practice, because it allows you to start
	// [Context.Shutdown] from any place in your code, not only from main.
	// It can be useful in some cases (e.g. handle [http.Server.ListenAndServe] error), but it's not recommended.
	Cancel()

	// Shutdown cancels an underlying context, then calls every added function with shutdown timeout in parallel.
	// It will return an error if the timeout is exceeded or if any of the shutdown functions returns an error.
	Shutdown() error
}

// File is an interface with operations that need to be called during file closing.
type File interface {
	Sync() error
	Close() error
}

// Start starts a new application with a given run function and logger. The run function should be non-blocking.
// It should initialize the application, start workers in separate goroutines and return an error in case of initialization failure.
// Start will wait for interrupt signals and then call [Context.Shutdown]. It uses the logger to log run() errors.
// The run function accepts [Context] as an argument, so you can add shutdown and cancel methods to it.
// If an error occurs during run, it will log it and exit with code 1.
// [AutoShutdown], [Exit], [WithLogger] options are no-op because they are applied by default.
// Option [WithNoWait] will not call [Context.Wait] at the end of the [Start] function,
// so [Start] will return immediately after the run() function call.
func Start(run func(Context) error, log Logger, opts ...Option) {
	var err error

	opt := parseOptions(opts...)
	opt.Log = log
	opt.OuterErr = &err
	opt.Exit = true

	ctx := NewWithOptions(opt)
	defer ctx.Shutdown()
	defer recoverPanic(log)

	if err = run(ctx); err != nil && log != nil {
		log.Error("cannot run application", "error", err)
		return
	}

	if !opt.NoWait {
		ctx.Wait()
	}
}

var _ context.Context = (*Contem)(nil)
var _ Context = (*Contem)(nil)

type Contem struct {
	funcs       []ShutdownFunc
	fileClosers []CloseFunc

	ctx    context.Context
	cancel func()

	shutdownTimeout time.Duration

	log           Logger
	outerErr      *error
	exitErrorCode int
	noParallel    bool
	exit          bool
	logging       bool
	noFiles       bool
	regularOrder  bool

	isClosed atomic.Bool
	mu       sync.Mutex
}

// New returns a ready to use [Context] with a created [signal.NotifyContext]
// listening to [syscall.SIGINT] and [syscall.SIGTERM] signals by default.
// You also can provide your custom signals, custom logger or other options.
func New(opts ...Option) *Contem {
	return NewWithOptions(parseOptions(opts...))
}

// NewWithOptions returns a ready to use [Context] with a created [signal.NotifyContext]
// listening to [syscall.SIGINT] and [syscall.SIGTERM] signals by default.
// You also can provide your custom signals, custom logger or other options.
func NewWithOptions(opts Options) *Contem {
	if opts.BaseCtx == nil {
		opts.BaseCtx = context.Background()
	}

	if len(opts.Signals) == 0 {
		opts.Signals = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	}

	ctx, cancel := signal.NotifyContext(opts.BaseCtx, opts.Signals...)

	ct := &Contem{
		ctx:             ctx,
		cancel:          cancel,
		log:             opts.Log,
		outerErr:        opts.OuterErr,
		exitErrorCode:   opts.ExitErrorCode,
		noParallel:      opts.NoParallel,
		exit:            opts.Exit,
		logging:         opts.Log != nil,
		noFiles:         opts.DontCloseFiles,
		regularOrder:    opts.RegularFileOrder,
		shutdownTimeout: opts.ShutdownTimeout,
	}

	if opts.AutoShutdown {
		go func() {
			defer recoverPanic(ct.log)

			<-ctx.Done()
			if err := ct.Shutdown(); err != nil && ct.log != nil {
				ct.log.Error("cannot shutdown", "error", err)
			}
		}()
	}

	return ct
}

// NewEmpty returns a dummy [Context] with [context.Background] context. It is useful for tests.
func NewEmpty() *Contem {
	return &Contem{ctx: context.Background(), cancel: func() {}}
}

// Empty returns a dummy [Context] with [context.Background] context. It is useful for tests.
func Empty() *Contem {
	return NewEmpty()
}

// Add adds a shutdown function to the list of functions that will be called in the [Context.Shutdown] method.
func (ct *Contem) Add(f ShutdownFunc) {
	if f == nil {
		return // Silently ignore nil functions to prevent panics
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.funcs = append(ct.funcs, f)
}

// AddClose adds a close function (from [io.Closer]) to the list of functions
// that will be called in the [Context.Shutdown] method.
func (ct *Contem) AddClose(f CloseFunc) {
	if f == nil {
		return // Silently ignore nil functions to prevent panics
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.funcs = append(ct.funcs, func(context.Context) error {
		return f()
	})
}

// AddFunc adds a plain function to the list of functions that will be called in the [Context.Shutdown] method.
func (ct *Contem) AddFunc(f func()) {
	if f == nil {
		return // Silently ignore nil functions to prevent panics
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.funcs = append(ct.funcs, func(context.Context) error {
		f()
		return nil
	})
}

// AddFile adds a [File] to the list of functions that will be called in the [Context.Shutdown] method
// after all other closing methods (they can produce output to files, for example).
func (ct *Contem) AddFile(f File) {
	if f == nil {
		return // Silently ignore nil files to prevent panics
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	closer := func() error {
		var errs []error
		if err := f.Sync(); err != nil {
			errs = append(errs, fmt.Errorf("sync: %w", err))
		}
		if err := f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close: %w", err))
		}
		return joinErrors(errs)
	}

	if ct.regularOrder {
		ct.funcs = append(ct.funcs, func(ctx context.Context) error {
			return closer()
		})
	} else {
		ct.fileClosers = append(ct.fileClosers, closer)
	}
}

// SetValue sets a value to the underlying context. You can get this value using the [Context.Value] method.
// It updates the original context.
func (ct *Contem) SetValue(key, value any) Context {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.ctx = context.WithValue(ct.ctx, key, value)
	return ct
}

// Wait blocks until the channel is closed (receiving [syscall.SIGINT] and [syscall.SIGTERM] signals by default).
// It should be used in the main() function after application start to wait for an interruption.
func (ct *Contem) Wait() {
	if ch := ct.ctx.Done(); ch != nil {
		<-ch
	}
	// If ctx.Done() returns nil, the context is never cancelled (like context.Background()),
	// so we return immediately rather than blocking forever
}

// Cancel cancels an underlying context. Using this method is a bad practice, because it allows you to start
// Context.Shutdown from any place in your code, not only from main.
// It can be useful in some cases (e.g. handle [http.ListenAndServe] error), but it's not recommended.
func (ct *Contem) Cancel() {
	ct.cancel()
}

// Shutdown cancels an underlying context, then calls every added function with [ShutdownTimeout] in parallel.
// It will return an error if the timeout is exceeded or if any of the shutdown functions returns an error.
func (ct *Contem) Shutdown() error {
	defer recoverPanic(ct.log)

	ct.mu.Lock()
	defer ct.mu.Unlock()

	if ct.isClosed.Load() {
		return nil
	}
	ct.isClosed.Store(true)
	ct.cancel()

	if ct.logging && ct.log != nil {
		ct.log.Info("starting shutdown")
	}

	var (
		start = time.Now()
		ws    = newWaiterSet(ct.log)
	)

	if ct.shutdownTimeout == 0 {
		ct.shutdownTimeout = GetDefaultShutdownTimeout()
	}

	errs := ct.shutdown(ws, start)

	errsFiles := ct.closeFiles(ws, start)
	if len(errsFiles) > 0 {
		errs = append(errs, errsFiles...)
	}

	serr := joinErrors(errs)
	if serr != nil {
		if ct.logging && ct.log != nil {
			ct.log.Error("cannot shutdown", "error", serr)
		}
		ct.outerErr = &serr // we will os.Exit(1) in any way
	}

	// if you add here recoverPanic function it will not work
	if panicErr := recover(); panicErr != nil {
		stack := debug.Stack()
		if ct.logging && ct.log != nil {
			ct.log.Error(string(stack), "panic", panicErr)
		} else {
			fmt.Fprintln(os.Stderr, "panic:", panicErr, "\n\n", string(stack))
		}
	}

	if ct.exit {
		time.Sleep(100 * time.Millisecond) // wait for flush
		if ct.outerErr != nil && *ct.outerErr != nil {
			os.Exit(ct.exitErrorCode)
		}
		os.Exit(0)
	}

	return serr
}

// Deadline returns the time when work done on behalf of this context should be canceled.
// Deadline returns ok==false when no deadline is set.
func (ct *Contem) Deadline() (time.Time, bool) {
	return ct.ctx.Deadline()
}

// Done returns a channel that will be closed (after receiving [syscall.SIGINT] or [syscall.SIGTERM] signal by default).
func (ct *Contem) Done() <-chan struct{} {
	return ct.ctx.Done()
}

// Err returns nil if Done is not yet closed, if Done is closed, Err returns a non-nil error explaining why.
func (ct *Contem) Err() error {
	return ct.ctx.Err()
}

// Value returns the value associated with this context for key, or nil if no value is associated with key.
func (ct *Contem) Value(key any) any {
	return ct.ctx.Value(key)
}

func (ct *Contem) shutdown(ws *waiterSet, start time.Time) []error {
	var errs []error
	if ct.noParallel {
		for _, f := range ct.funcs {
			if err := f(ct.ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errs
	}

	shutdownCtx, cancel := context.WithTimeout(ct.ctx, ct.shutdownTimeout)
	defer cancel()

	for _, f := range ct.funcs {
		ws.add(shutdownCtx, f)
	}

	err := ws.await(start, ct.shutdownTimeout)
	if err != nil {
		errs = append(errs, fmt.Errorf("shutdown: %w", err))
	}

	return errs
}

func (ct *Contem) closeFiles(ws *waiterSet, start time.Time) []error {
	if ct.noFiles {
		return nil
	}

	if len(ct.fileClosers) == 0 {
		return nil
	}

	var errs []error
	if ct.noParallel {
		for _, f := range ct.fileClosers {
			if err := f(); err != nil {
				errs = append(errs, err)
			}
		}
		return errs
	}

	for _, f := range ct.fileClosers {
		f := f // capture loop variable
		ws.add(context.Background(), func(context.Context) error {
			return f()
		})
	}

	timeout := max(ct.shutdownTimeout-time.Since(start), ct.shutdownTimeout/5)

	err := ws.await(start, timeout)
	if err != nil {
		errs = append(errs, fmt.Errorf("close files: %w", err))
	}

	return errs
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
		currentTimeout := max(timeout-time.Since(start), 0)
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
				if w.err != nil {
					w.err = fmt.Errorf("%w: %s", w.err, panicErr)
				} else {
					w.err = fmt.Errorf("%s", panicErr)
				}
				if l != nil {
					stack := debug.Stack()
					l.Error(string(stack), "error", w.err)
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

	var builder strings.Builder
	first := true

	for _, err := range errs {
		if err == nil {
			continue
		}
		msg := err.Error()
		if msg == "" {
			continue
		}
		if !first {
			builder.WriteString("; ")
		}
		builder.WriteString(msg)
		first = false
	}

	if builder.Len() == 0 {
		return nil
	}

	return errors.New(builder.String())
}

func recoverPanic(l Logger) {
	if panicErr := recover(); panicErr != nil {
		stack := debug.Stack()
		if l != nil {
			l.Error(string(stack), "panic", panicErr)
		} else {
			fmt.Fprintln(os.Stderr, "panic:", panicErr, "\n\n", string(stack))
		}
	}
}
