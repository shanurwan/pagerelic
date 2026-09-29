// Command pagerelic recovers and analyses PostgreSQL data at the page level.
// See README.md.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/shanurwan/pagerelic/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	os.Exit(cli.Run(ctx, os.Args, os.Stdout, os.Stderr))
}
