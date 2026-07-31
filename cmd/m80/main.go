// Command m80 runs the Lambda MicroVMs emulator.
//
// Point any AWS SDK at it with an endpoint override and it answers the
// service's wire protocol. Operations that are routed but not yet implemented
// answer 501, and /_m80/health reports exactly which those are.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/intentius/m80"
	"github.com/intentius/m80/internal/api"
	"github.com/intentius/m80/internal/clock"
	"github.com/intentius/m80/internal/images"
	"github.com/intentius/m80/internal/managedimages"
	"github.com/intentius/m80/internal/store"
	"github.com/intentius/m80/internal/vms"
)

func main() {
	addr := flag.String("addr", ":4290", "listen address")
	logLevel := flag.String("log-level", "info", "debug, info, warn or error")
	showVersion := flag.Bool("version", false, "print the version and exit")
	// One hop of a build state machine. Short enough that a conformance run
	// is quick, long enough that a demo shows the intermediate states rather
	// than jumping straight to CREATED.
	buildDelay := flag.Duration("build-delay", time.Second, "delay per build state transition")
	flag.Parse()

	if *showVersion {
		fmt.Println(m80.Version)
		return
	}

	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "bad -log-level %q\n", *logLevel)
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	st := store.New()
	clk := clock.Real{}
	srv := api.NewServer(clk, st, m80.Version)
	managedimages.Register(srv)
	imageSvc := images.NewService(clk, st, *buildDelay)
	vmSvc := vms.NewService(clk, st, *buildDelay)
	// Each side asks the other one question: images refuses to delete while a
	// VM runs, and vms refuses to run an image with nothing built.
	images.Register(srv, imageSvc, vmSvc)
	vms.Register(srv, vmSvc, imageSvc)

	impl := len(srv.Implemented())
	log.Info("m80 starting",
		"version", m80.Version,
		"addr", *addr,
		"operations", fmt.Sprintf("%d/%d implemented", impl, len(api.Routes)))

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Shut down on a signal rather than dying mid-response, so a container
	// restart does not look like a service fault to whatever is polling it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		log.Error("listen failed", "error", err)
		os.Exit(1)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "error", err)
		os.Exit(1)
	}
}
