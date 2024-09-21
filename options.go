package contem

import (
	"context"
	"os"
)

// Option is a function to change [Context] behaviour.
type Option func(*Options)

// Logger is an interface of a structural logger that is used in [Context.Shutdown] method.
// It is used to log messages in case of error or during shutdown if you provide [WithLogger] option.
type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

// Logze is an interface of a logger from github.com/maxbolgarin/logze.
// You can add it using [WithLogze] method as logger for [Context.Shutdown].
type Logze interface {
	Info(msg string, args ...any)
	Error(err error, msg string, args ...any)
}

// Options contains all options to change [Context] behaviour.
type Options struct {
	// BaseCtx is the base context for [Context] underlying context.
	// It is used to cancel [Context]'s underlying context.
	BaseCtx context.Context
	// Signals is a list of signals that triggers context close, default is [syscall.SIGINT] and [syscall.SIGTERM].
	Signals []os.Signal
	// Log is a logger that is used in [Context.Shutdown] method to log errors and info messages.
	Log Logger
	// OuterErr is an error for [Options.Exit] option to exit with 1 code if it is not nil even after successful shutdown.
	OuterErr *error
	// Exit calls [os.Exit] at the end of [Context.Shutdown] method with 1 code if there are errors, zero code otherwise.
	Exit bool
	// ExitErrorCode is an error code to exit if [Options.Exit] option is true and an error occurred.
	ExitErrorCode int
	// AutoShutdown will trigger [Context.Shutdown] when underlying context is closed.
	AutoShutdown bool
	// NoParallel will disable parallel shutdowns in [Context.Shutdown].
	NoParallel bool
	// RegularFileOrder will use regular file closing order in [Context.Shutdown].
	// In default files are closed after all other shutdown/closing methods. With this option they are closed with them.
	RegularFileOrder bool
	// DontCloseFiles ignores file closing at the end of [Context.Shutdown].
	DontCloseFiles bool
}

// AutoShutdown will trigger [Context.Shutdown] when underlying context is closed.
// It runs a goroutine to check ctx.Done().
// It is recommended to use [WithLogger] to get info about shutdown and [Exit] to stop the application after shutdown.
func AutoShutdown() Option {
	return func(s *Options) {
		s.AutoShutdown = true
	}
}

// Exit calls [os.Exit] at the end of [Context.Shutdown] method with 1 code if there are errors, zero code otherwise.
// It accepts outer error to exit with 1 code if it is not nil even after successful shutdown.
// It also logs "cannot shutdown" message if shutdown failed and waits for 100ms to flush before exiting the program.
// You can provide errorCode to call [os.Exit] with it in case of error.
func Exit(err *error, errorCode ...int) Option {
	return func(s *Options) {
		s.OuterErr = err
		s.Exit = true
		s.ExitErrorCode = 1
		if len(errorCode) > 0 {
			s.ExitErrorCode = errorCode[0]
		}
	}
}

// DontCloseFiles ignores file closing at the end of [Context.Shutdown],
// so you rely on GC, that will close files after returning from main.
func DontCloseFiles() Option {
	return func(s *Options) {
		s.DontCloseFiles = true
	}
}

// RegularCloseFilesOrder adds file closers to the general order with another shutdown/closing methods.
// In default files are closed after all other shutdown/closing methods. With this option they are closed with them.
func RegularCloseFilesOrder() Option {
	return func(s *Options) {
		s.RegularFileOrder = true
	}
}

// NoParallel disables parallel calling of shutdown methods.
// With this option [Context.Shutdown] closes them sequentially in the order they were added.
func NoParallel() Option {
	return func(s *Options) {
		s.NoParallel = true
	}
}

// WithBaseContext sets the base context for [Context] underlying context.
// Cancelling of this context will cancel [Context]'s underlying context.
func WithBaseContext(ctx context.Context) Option {
	return func(s *Options) {
		s.BaseCtx = ctx
	}
}

// WithLogger uses given logger in the [Context.Shutdown] method to log errors and info messages.
func WithLogger(l Logger) Option {
	return func(s *Options) {
		s.Log = l
	}
}

// WithLogger uses given logze logger in the [Context.Shutdown] method to log errors and info messages.
func WithLogze(l Logze) Option {
	return WithLogger(LogzeToLogger(l))
}

// WithSignals sets signals that triggers context close, default is [syscall.SIGINT] and [syscall.SIGTERM].
func WithSignals(sig ...os.Signal) Option {
	return func(s *Options) {
		s.Signals = sig
	}
}

// LogzeToLogger converts [Logze] (logger from github.com/maxbolgarin/logze) to [Logger].
func LogzeToLogger(l Logze) Logger {
	return logzeWrapper{l}
}

func parseOptions(opts ...Option) Options {
	res := Options{}
	for _, optFunc := range opts {
		optFunc(&res)
	}
	return res
}

type logzeWrapper struct {
	Logze
}

func (l logzeWrapper) Error(msg string, args ...any) {
	l.Logze.Error(nil, msg, args...)
}
