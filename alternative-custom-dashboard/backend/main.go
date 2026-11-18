package main

import (
	"context"
	"log"
	"net/http"
	"time"
	_ "time/tzdata" // so timezones work in a minimal container
)

func main() {
	cfg := loadConfig()
	ctx := context.Background()

	store, err := newStore(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	catalog, err := newCatalog(cfg)
	if err != nil {
		log.Fatalf("catalog: %v", err)
	}
	if data, err := catalog.Load(ctx); err != nil {
		log.Printf("warning: could not load hubs and temples yet: %v", err)
	} else {
		log.Printf("catalog (%s): %d hubs, %d temples", cfg.CatalogSource, len(data.Hubs), len(data.Temples))
	}
	if cfg.AgentToken == "" {
		log.Printf("warning: AGENT_TOKEN is not set, so anyone who can reach this server can register devices and send data")
	}

	// keep the table small: delete readings older than the retention period, once an hour
	go func() {
		for {
			if n, err := store.Prune(ctx, cfg.RetentionDays); err != nil {
				log.Printf("prune: %v", err)
			} else if n > 0 {
				log.Printf("pruned %d old readings", n)
			}
			time.Sleep(time.Hour)
		}
	}()

	sched, err := parseSchedule(cfg.StreamWindows, cfg.Timezone, cfg.EarlyMinutes)
	if err != nil {
		log.Fatalf("schedule: %v", err)
	}
	log.Printf("schedule: %s (%s)", sched.Describe(time.Now()).Text, cfg.Timezone)

	srv := &Server{cfg: cfg, store: store, catalog: catalog, sched: sched}
	log.Printf("stream-monitor backend listening on :%s", cfg.Port)
	log.Fatal(http.ListenAndServe(":"+cfg.Port, srv.routes()))
}
