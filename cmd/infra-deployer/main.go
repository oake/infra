package main

import (
	"context"
	"flag"
	"github.com/oake/infra/internal/api"
	"github.com/oake/infra/internal/deployer"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	hub := flag.String("hub", os.Getenv("INFRA_HUB_URL"), "hub HTTPS URL")
	repository := flag.String("repository", os.Getenv("INFRA_REPOSITORY"), "flake repository (owner/repo)")
	tokenFile := flag.String("token-file", os.Getenv("INFRA_TOKEN_FILE"), "Traefik bearer token file (or INFRA_TOKEN)")
	queue := flag.String("queue", "/var/lib/infra-deployer/queue.json", "durable execution queue")
	interval := flag.Duration("interval", 12*time.Second, "poll interval")
	concurrency := flag.Int("concurrency", 2, "concurrent hosts")
	flag.Parse()
	token, e := api.Token(os.Getenv("INFRA_TOKEN"), *tokenFile)
	if e != nil {
		log.Fatal(e)
	}
	client, e := api.NewClient(*hub, token)
	if e != nil {
		log.Fatal(e)
	}
	executor := deployer.Executor{}
	r, e := deployer.Open(client, *repository, *queue, executor.Execute)
	if e != nil {
		log.Fatal(e)
	}
	defer r.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e = r.Run(ctx, *interval, *concurrency); e != nil {
		log.Fatal(e)
	}
}
