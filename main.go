package main

import (
	"embed"
	"flag"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"dante-player/api"
	"dante-player/config"
	"dante-player/engine"
)

//go:embed web/*
var webFS embed.FS

func main() {
	configPath := flag.String("config", "", "Path to config file (optional)")
	httpPort := flag.Int("port", 8080, "HTTP server port")
	pipeDir := flag.String("pipe-dir", "/tmp/dante_player", "Directory for audio FIFO pipes")
	danteName := flag.String("dante-name", "Dante-Pi", "Dante device advertised name")
	flag.Parse()

	log.Println("==================================================")
	log.Println("  Dante Audio Hub & Player via Inferno (Go Engine)")
	log.Println("==================================================")

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Printf("Warning loading config: %v, using defaults", err)
		cfg = config.DefaultConfig()
	}

	if envPort := os.Getenv("HTTP_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil && p > 0 {
			cfg.HTTPPort = p
		}
	}
	if *httpPort != 8080 || cfg.HTTPPort == 0 {
		cfg.HTTPPort = *httpPort
	}
	if *pipeDir != "" {
		cfg.PipeDir = *pipeDir
	}
	if *danteName != "" {
		cfg.DanteName = *danteName
	}

	// Prepare static web assets sub filesystem
	strippedWebFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("Failed to initialize web filesystem: %v", err)
	}

	// Initialize Playback Engine
	mgr := engine.NewPlaybackManager(cfg)

	// Initialize REST/SSE API Server
	server := api.NewServer(cfg, mgr, strippedWebFS)

	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("\nShutting down Dante Web Player...")
		mgr.StopAll()
		os.Exit(0)
	}()

	if err := server.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
