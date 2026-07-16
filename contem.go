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
// If an error occurs during run (or run panics), it will log it and exit with code 1.
// [Exit] and [WithLogger] options are no-op because they are applied by default.
// Option [WithNoWait] will not call [Context.Wait] at the end of the [Start] function:
// resources are cleaned up right after the run() function call and Start returns without calling [os.Exit].
func Start(run func(Context) error, log Logger, opts ...Option) {
	var err error

	opt := parseOptions(opts...)
	opt.Log = log
	opt.OuterErr = &err
	// With NoWait Start must return to the caller as documented, so it cannot call os.Exit.
	// ExitErrorCode defaults to 1 in NewWithOptions when Exit is set.
	opt.Exit = !opt.NoWait

	ctx := NewWithOptions(opt)
	defer ctx.Shutdown()
	defer func() {
		if panicErr := recover(); panicErr != nil {
			logPanic(log, panicErr)
			err = fmt.Errorf("panic: %v", panicErr)
		}
	}()

	if err = run(ctx); err != nil {
		if log != nil {
			log.Error("cannot run application", "error", err)
		}
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
	// ctxMu guards ctx, which is replaced by SetValue and read by the context.Context methods.
	ctxMu sync.RWMutex

	shutdownTimeout time.Duration

	log           Logger
	outerErr      *error
	exitErrorCode int
	noParallel    bool
	exit          bool
	noFiles       bool
	regularOrder  bool

	isClosed atomic.Bool
	// shutdownDone is closed when the winning Shutdown call finishes,
	// so concurrent Shutdown calls can block until cleanup is complete.
	shutdownDone chan struct{}
	mu           sync.Mutex
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

	if opts.Exit && opts.ExitErrorCode == 0 {
		opts.ExitErrorCode = 1
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
		noFiles:         opts.DontCloseFiles,
		regularOrder:    opts.RegularFileOrder,
		shutdownTimeout: opts.ShutdownTimeout,
		shutdownDone:    make(chan struct{}),
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
	return &Contem{ctx: context.Background(), cancel: func() {}, shutdownDone: make(chan struct{})}
}

// Empty returns a dummy [Context] with [context.Background] context. It is useful for tests.
func Empty() *Contem {
	return NewEmpty()
}

// Add adds a shutdown function to the list of functions that will be called in the [Context.Shutdown] method.
// Functions added after [Context.Shutdown] has started are ignored.
func (ct *Contem) Add(f ShutdownFunc) {
	if f == nil {
		return // Silently ignore nil functions to prevent panics
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	if ct.isClosed.Load() {
		return // Shutdown already started, this function would never be called
	}

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

	if ct.isClosed.Load() {
		return // Shutdown already started, this function would never be called
	}

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

	if ct.isClosed.Load() {
		return // Shutdown already started, this function would never be called
	}

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

	if ct.isClosed.Load() {
		return // Shutdown already started, this file would never be closed
	}

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
	if key == nil {
		return ct // Silently ignore nil keys, context.WithValue panics on them
	}

	ct.ctxMu.Lock()
	defer ct.ctxMu.Unlock()

	ct.ctx = context.WithValue(ct.ctx, key, value)
	return ct
}

// Wait blocks until the channel is closed (receiving [syscall.SIGINT] and [syscall.SIGTERM] signals by default).
// It should be used in the main() function after application start to wait for an interruption.
func (ct *Contem) Wait() {
	if ch := ct.context().Done(); ch != nil {
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
// Functions added after Shutdown has started are not called.
// If Shutdown is called concurrently, only the first call performs the cleanup;
// the other calls block until it is finished and return nil.
func (ct *Contem) Shutdown() error {
	defer recoverPanic(ct.log)

	ct.mu.Lock()
	if ct.isClosed.Load() {
		done := ct.shutdownDone
		ct.mu.Unlock()
		if done != nil {
			<-done // block until the winning Shutdown call finishes
		}
		return nil
	}
	ct.isClosed.Store(true)

	funcs := ct.funcs
	fileClosers := ct.fileClosers
	ct.funcs, ct.fileClosers = nil, nil

	timeout := ct.shutdownTimeout
	if timeout == 0 {
		timeout = GetDefaultShutdownTimeout()
	}
	done := ct.shutdownDone
	// Release the lock before running shutdown functions, so they can safely
	// call Add* and SetValue without deadlocking (especially in NoParallel mode).
	ct.mu.Unlock()

	if done != nil {
		defer close(done)
	}

	ct.cancel()

	if ct.log != nil {
		ct.log.Info("starting shutdown")
	}

	var (
		start = time.Now()
		ws    = newWaiterSet(ct.log)
	)

	// The underlying context is already canceled at this point, so the shutdown context
	// must not inherit its cancellation (only its values) — otherwise every shutdown
	// function would receive a dead context and e.g. http.Server.Shutdown would not drain.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ct.context()), timeout)
	defer cancel()

	errs := ct.shutdown(shutdownCtx, ws, funcs, start, timeout)
	errs = append(errs, ct.closeFiles(shutdownCtx, ws, fileClosers, start, timeout)...)

	serr := joinErrors(errs)
	if serr != nil && ct.log != nil {
		ct.log.Error("cannot shutdown", "error", serr)
	}

	// When Shutdown runs as a deferred call of a panicking function (`defer ctx.Shutdown()`
	// and a panic in main), only a recover() called directly in Shutdown's body can catch
	// that panic — a nested call like recoverPanic() would not work here.
	if panicErr := recover(); panicErr != nil {
		logPanic(ct.log, panicErr)
	}

	if ct.exit {
		time.Sleep(100 * time.Millisecond) // wait for flush
		if serr != nil || (ct.outerErr != nil && *ct.outerErr != nil) {
			os.Exit(ct.exitErrorCode)
		}
		os.Exit(0)
	}

	return serr
}

// Deadline returns the time when work done on behalf of this context should be canceled.
// Deadline returns ok==false when no deadline is set.
func (ct *Contem) Deadline() (time.Time, bool) {
	return ct.context().Deadline()
}

// Done returns a channel that will be closed (after receiving [syscall.SIGINT] or [syscall.SIGTERM] signal by default).
func (ct *Contem) Done() <-chan struct{} {
	return ct.context().Done()
}

// Err returns nil if Done is not yet closed, if Done is closed, Err returns a non-nil error explaining why.
func (ct *Contem) Err() error {
	return ct.context().Err()
}

// Value returns the value associated with this context for key, or nil if no value is associated with key.
func (ct *Contem) Value(key any) any {
	return ct.context().Value(key)
}

func (ct *Contem) context() context.Context {
	ct.ctxMu.RLock()
	defer ct.ctxMu.RUnlock()
	return ct.ctx
}

func (ct *Contem) shutdown(ctx context.Context, ws *waiterSet, funcs []ShutdownFunc, start time.Time, timeout time.Duration) []error {
	var errs []error
	if ct.noParallel {
		for _, f := range funcs {
			if err := f(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errs
	}

	for _, f := range funcs {
		ws.add(ctx, f)
	}

	err := ws.await(start, timeout)
	if err != nil {
		errs = append(errs, fmt.Errorf("shutdown: %w", err))
	}

	return errs
}

func (ct *Contem) closeFiles(ctx context.Context, ws *waiterSet, fileClosers []CloseFunc, start time.Time, timeout time.Duration) []error {
	if ct.noFiles {
		return nil
	}

	if len(fileClosers) == 0 {
		return nil
	}

	var errs []error
	if ct.noParallel {
		for _, f := range fileClosers {
			if err := f(); err != nil {
				errs = append(errs, err)
			}
		}
		return errs
	}

	for _, f := range fileClosers {
		f := f // capture loop variable
		ws.add(ctx, func(context.Context) error {
			return f()
		})
	}

	// Files get the remaining shutdown budget, but at least timeout/5,
	// counted from now — the first phase already consumed time since start.
	fileTimeout := max(timeout-time.Since(start), timeout/5)

	err := ws.await(time.Now(), fileTimeout)
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
				w.err = fmt.Errorf("%s", panicErr)
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

// joinedError joins multiple errors into one, keeping errors.Is/As working
// through Unwrap and formatting the message as "err1; err2; ...".
type joinedError struct {
	errs []error
}

func (e *joinedError) Error() string {
	var builder strings.Builder
	for i, err := range e.errs {
		if i > 0 {
			builder.WriteString("; ")
		}
		builder.WriteString(err.Error())
	}
	return builder.String()
}

func (e *joinedError) Unwrap() []error {
	return e.errs
}

func joinErrors(errs []error) error {
	var nonNil []error
	for _, err := range errs {
		if err == nil || err.Error() == "" {
			continue
		}
		nonNil = append(nonNil, err)
	}

	switch len(nonNil) {
	case 0:
		return nil
	case 1:
		return nonNil[0]
	default:
		return &joinedError{errs: nonNil}
	}
}

func recoverPanic(l Logger) {
	if panicErr := recover(); panicErr != nil {
		logPanic(l, panicErr)
	}
}

func logPanic(l Logger, panicErr any) {
	stack := debug.Stack()
	if l != nil {
		l.Error(string(stack), "panic", panicErr)
	} else {
		fmt.Fprintln(os.Stderr, "panic:", panicErr, "\n\n", string(stack))
	}
}
