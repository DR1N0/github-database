package server

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	"os"

	"github.com/DR1N0/github-database/ghdb"
)

var (
	tableDB  ghdb.TableDB
	isOnline bool
	logger   = log.New(os.Stderr, "[ghdb-demo] ", log.LstdFlags)
)

func openDB(ctx context.Context, offline bool, baseline fs.FS) error {
	opt := ghdb.NewOption(baseline).Logger(logger)
	if !offline {
		opt = opt.Token(func() string { return os.Getenv("GITHUB_TOKEN") })
	}
	var err error
	tableDB, err = opt.OpenTable(ctx)
	if err != nil {
		return fmt.Errorf("open DB: %w", err)
	}
	isOnline = tableDB.IsOnline()
	return nil
}
