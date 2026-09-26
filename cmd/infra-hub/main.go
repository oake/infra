package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/oake/infra/internal/hub"
	"github.com/oake/infra/web"
)

func main() {
	if e := run(); e != nil {
		slog.Error("infra-hub stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	port := flag.Int("http-port", 8787, "HTTP port (listens on 0.0.0.0)")
	data := flag.String("data", "/var/lib/infra-hub", "state directory")
	inbox := flag.String("inbox", "", "Buildbot inbox directory (defaults to DATA/inbox)")
	dix := flag.String("dix", "dix", "dix snapshot-capable executable")
	flag.Parse()
	if *port < 1 || *port > 65535 {
		return fmt.Errorf("http-port must be between 1 and 65535")
	}
	listen := fmt.Sprintf("0.0.0.0:%d", *port)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	store, e := hub.Open(ctx, filepath.Join(*data, "infra.db"))
	if e != nil {
		return e
	}
	defer store.DB.Close()
	server := &hub.Server{Store: store, Root: *data, Dix: *dix}
	e = store.Update(ctx, func(s *hub.State) error {
		s.Reconcile(time.Now().UTC())
		return nil
	})
	if e != nil {
		return e
	}
	if *inbox == "" {
		*inbox = filepath.Join(*data, "inbox")
	}
	if err := os.MkdirAll(*inbox, 0770); err != nil {
		return err
	}
	token := os.Getenv("GITHUB_TOKEN")
	server.EnableGit(token)
	go server.RunRepositories(ctx)
	go server.RunInbox(ctx, *inbox)
	server.GitHub = &hub.GitHub{Token: token}
	interval := time.Minute
	if token == "" {
		interval = 30 * time.Minute
	}
	go func() {
		for {
			if err := server.PollPullRequests(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("GitHub PR poll", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}()
	srv := &http.Server{Addr: listen, Handler: server.Handler(web.Handler()), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: time.Minute}
	errs := make(chan error, 1)
	go func() { errs <- srv.ListenAndServe() }()
	slog.Info("infra is ready", "url", "http://"+listen)
	for {
		select {
		case <-ctx.Done():
			shutdown, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			return srv.Shutdown(shutdown)
		case e := <-errs:
			return e

		}
	}
}
