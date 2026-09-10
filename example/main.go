package main

import (
	"context"
	"embed"
	"example/server"
	"flag"
	"io/fs"
	"log"
	"time"
)

//go:embed demo-database
var embeddedFS embed.FS

func main() {
	offline := flag.Bool("offline", false, "run without GitHub (in-memory only)")
	flag.Parse()

	sub, err := fs.Sub(embeddedFS, "demo-database")
	if err != nil {
		log.Fatalf("embed sub: %v", err)
	}
	startupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	server.RunServer(startupCtx, *offline, sub)
}
