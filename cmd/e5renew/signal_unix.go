//go:build unix

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// reloadOnSIGHUP calls reload for every SIGHUP until ctx ends.
func reloadOnSIGHUP(ctx context.Context, reload func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				reload()
			}
		}
	}()
}
