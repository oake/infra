package main

import (
	"context"
	"flag"
	"log"
	"os"
	"runtime"
	"time"

	"github.com/oake/infra/internal/api"
	"github.com/oake/infra/internal/beacon"
)

func main() {
	hub := flag.String("hub", os.Getenv("INFRA_HUB_URL"), "hub HTTPS URL")
	host := flag.String("host", os.Getenv("INFRA_HOST"), "repository-qualified host ID")
	tokenFile := flag.String("token-file", os.Getenv("INFRA_TOKEN_FILE"), "bearer token file for Traefik (or INFRA_TOKEN)")
	flag.Parse()
	if *host == "" {
		log.Fatal("host is required")
	}
	token, e := api.Token(os.Getenv("INFRA_TOKEN"), *tokenFile)
	if e != nil {
		log.Fatal(e)
	}
	c, e := api.NewClient(*hub, token)
	if e != nil {
		log.Fatal(e)
	}
	b, e := beacon.Collect(*host, runtime.GOOS, "/")
	if e != nil {
		log.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if e = c.Do(ctx, "POST", "/api/beacon", b, nil); e != nil {
		log.Fatal(e)
	}
}
