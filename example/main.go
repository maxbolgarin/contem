package main

import (
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/maxbolgarin/contem"
)

var log = slog.Default()

func main() {
	// Error to pass into contem.Context for os.Exit(1) in case of run() or Shutdown() error
	var err error

	// defer Shutdown to close resources in any case (don't expect os.Exit outside contem)
	ctx := contem.New(contem.WithLogger(log), contem.Exit(&err))
	defer ctx.Shutdown()

	// Create a single starting function and don't rewrite address of the err variable
	if err = run(ctx); err != nil {
		log.Error("cannot run application", "error", err)
		return
	}

	// Wait for the interrupt signal
	ctx.Wait()

	// Here contem will do:
	// 1. Close log file
	// 2. Remove log file
	// 3. Shutdown server
	// 4. os.Exit(0)
}

func run(ctx contem.Context) error {
	logFile, err := os.OpenFile("out.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	// Use AddClose instead of AddFile because we will remove file and we want to close it first
	// Files from AddFile will be closed in the end of Shutdown
	ctx.AddClose(logFile.Close)
	ctx.AddClose(func() error { return os.Remove("out.log") })

	lg := slog.New(slog.NewTextHandler(io.MultiWriter(logFile, os.Stderr), nil))
	*log = *lg

	srv := http.Server{Addr: ":8080"}
	ctx.Add(srv.Shutdown)

	go func() {
		log.Info("starting server")
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Error("listen and serve", "error", err)
			ctx.Cancel() // Cancel context and start shutdown because this is critical case
		}
	}()

	return nil
}
