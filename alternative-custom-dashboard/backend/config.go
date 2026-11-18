package main

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Port          string
	DatabaseURL   string
	CatalogSource string // "seed" or "kratos"
	SeedFile      string
	KratosBaseURL string
	KratosLang    string
	KratosHeader  string // optional "Header-Name: value" sent on every kratos call
	AgentToken    string // shared secret the agents send; empty means open (local testing only)
	OfflineAfter  float64
	RetentionDays int
	StreamWindows string // e.g. "07:00-12:00,16:00-20:00", or "always"
	Timezone      string
	EarlyMinutes  int
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func loadConfig() Config {
	days, err := strconv.Atoi(env("RETENTION_DAYS", "7"))
	if err != nil || days < 1 {
		days = 7
	}
	early, err := strconv.Atoi(env("EARLY_MINUTES", "30"))
	if err != nil || early < 0 {
		early = 30
	}
	return Config{
		Port:          env("PORT", "8080"),
		DatabaseURL:   env("DATABASE_URL", "postgres://monitor:monitor@localhost:5432/monitor?sslmode=disable"),
		CatalogSource: env("CATALOG_SOURCE", "seed"),
		SeedFile:      env("CATALOG_SEED_FILE", "seed/catalog.json"),
		KratosBaseURL: strings.TrimRight(env("KRATOS_BASE_URL", ""), "/"),
		KratosLang:    env("KRATOS_LANG", "en"),
		KratosHeader:  env("KRATOS_AUTH_HEADER", ""),
		AgentToken:    env("AGENT_TOKEN", ""),
		OfflineAfter:  90, // seconds without a report before a device counts as offline
		RetentionDays: days,
		StreamWindows: env("STREAM_WINDOWS", ""),
		Timezone:      env("TIMEZONE", "Asia/Kolkata"),
		EarlyMinutes:  early,
	}
}
