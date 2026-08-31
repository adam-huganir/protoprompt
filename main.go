package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"

	"protoprompt/internal/api"
	"protoprompt/internal/config"
	"protoprompt/internal/db"
	"protoprompt/internal/store"
)

//go:embed all:web
var webFS embed.FS

func main() {
	configPath := flag.String("config", "", "path to config file (default ./protoprompt.json)")
	dir := flag.String("dir", "./prompts", "prompt workspace directory")
	dbPath := flag.String("db", "protoprompt.db", "SQLite database file")
	port := flag.Int("port", 0, "listen port (overrides config)")
	host := flag.String("host", "", "listen host (overrides config)")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("protoprompt: %v", err)
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *host != "" {
		cfg.Host = *host
	}
	cfg.Dir = *dir

	sqlDB, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("protoprompt: open db: %v", err)
	}
	st := store.New(cfg.Dir, sqlDB)

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("protoprompt: web: %v", err)
	}
	srv := api.New(*cfg, sqlDB, st, web)

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	log.Printf("protoprompt listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
