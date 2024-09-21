package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/maxbolgarin/contem"
)

var log = slog.Default()

func main() {
	// Error to pass into contem.Context for os.Exit(1) in case of run() or Shutdown() error
	var err error

	ctx := contem.New(
		contem.WithLogger(log),          // Logger to log 'starting shutdown' and 'cannot shutdown'
		contem.Exit(&err),               // Exit with 1 code in case of run() or Shutdown() error
		contem.RegularCloseFilesOrder(), // Close files in the general order (firstly close, then delete)
		contem.NoParallel(),             // Close sequentially (firstly close, then delete)
	)

	// defer Shutdown to close resources in any case (don't expect os.Exit outside contem)
	defer ctx.Shutdown()

	// Create a single starting function and don't rewrite address of the err variable
	if err = run(ctx); err != nil {
		log.Error("cannot run application", "error", err)
		return
	}

	// Wait for the interrupt signal
	ctx.Wait()

	// Here contem will do:
	// 1. Shutdown server
	// 2. Close all closers
	// 3. Close log file
	// 4. Remove log file
	// 5. os.Exit(0)

	// Goroutine with ticker will also be stopped after recieving the interrupt signal.
}

func run(ctx contem.Context) error {
	srv := http.Server{Addr: ":8080"}
	ctx.Add(srv.Shutdown)

	// Dummy example, image you have real code here
	for i := range 10 {
		ctx.AddClose(new(ctx, i).Close)
	}

	logFile, err := os.OpenFile("out.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	// contem.RegularCloseFilesOrder() means that this file will closed with other sutdown methods
	// contem.NoParallel means that this file will be closed BEFORE it deletes
	ctx.AddFile(logFile)
	ctx.AddClose(func() error { return os.Remove("out.log") })

	lg := slog.New(slog.NewTextHandler(io.MultiWriter(logFile, os.Stderr), nil))
	*log = *lg

	go func() {
		log.Info("starting server")
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Error("listen and serve", "error", err)
			ctx.Cancel() // Cancel context and start shutdown because this is critical case
		}
	}()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				log.Info("5 seconds passed")
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

type testCloser struct {
	i int
}

func new(_ context.Context, i int) testCloser {
	return testCloser{i}
}

func (t testCloser) Close() error {
	log.Info("closing", "i", t.i)
	return nil
}
