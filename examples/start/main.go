package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/maxbolgarin/contem"
)

var log = slog.Default()

func main() {
	contem.Start(run, log)
}

func run(ctx contem.Context) error {
	srv := http.Server{Addr: ":8080"}
	ctx.Add(srv.Shutdown)

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
