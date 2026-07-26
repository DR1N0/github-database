package main

import (
	"embed"
	"example/server"
	"flag"
	"io/fs"
	"log"
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
	server.RunServer(*offline, sub)
}
