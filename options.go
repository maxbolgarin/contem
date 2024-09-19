package contem

import (
	"context"
	"os"
)

// Option is a function to change [Context] behaviour.
type Option func(*shutdownerOptions)

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

// DontCloseFiles ignores file closing at the end of [Context.Shutdown],
// so you rely on GC, that will close files after returning from main.
func DontCloseFiles() Option {
	return func(s *shutdownerOptions) {
		s.noFiles = true
	}
}

// WithLogger uses given logger in the [Context.Shutdown] method to log errors and info messages.
func WithLogger(l Logger) Option {
	return func(s *shutdownerOptions) {
		s.log = l
		s.logging = true
	}
}

// WithLogger uses given logze logger in the [Context.Shutdown] method to log errors and info messages.
func WithLogze(l Logze) Option {
	return func(s *shutdownerOptions) {
		s.log = logzeWrapper{l}
		s.logging = true
	}
}

// WithSignals sets signals that triggers context close, default is [syscall.SIGINT] and [syscall.SIGTERM].
func WithSignals(sig ...os.Signal) Option {
	return func(s *shutdownerOptions) {
		s.signals = sig
	}
}

// WithBaseContext sets the base context for [Context] underlying context.
// Cancelling of this context will cancel [Context] underlying context.
func WithBaseContext(ctx context.Context) Option {
	return func(s *shutdownerOptions) {
		s.baseCtx = ctx
	}
}

// AutoShutdown will trigger shutdown [Context.Shutdown] when underlying context is closed.
// It runs a goroutine to check ctx.Done().
// It is recommended to use [WithLogger] to get info about shutdown and [Exit] to stop the application after shutdown.
func AutoShutdown() Option {
	return func(s *shutdownerOptions) {
		s.auto = true
	}
}

// Exit calls os.Exit at the end of [Context.Shutdown] method with '1' code if there are errors, '0' otherwise.
// It accepts outer error to exit with '1' code if it is not nil even after successful shutdown.
// It also logs "cannot shutdown" message if shutdown failed and waits for 100ms to flush before exiting the program.
func Exit(err *error) Option {
	return func(s *shutdownerOptions) {
		s.outerErr = err
		s.exit = true
	}
}

type shutdownerOptions struct {
	baseCtx  context.Context
	outerErr *error
	signals  []os.Signal
	log      Logger
	auto     bool
	logging  bool
	noFiles  bool
	exit     bool
}

func parseOptions(opts ...Option) shutdownerOptions {
	res := shutdownerOptions{}
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
