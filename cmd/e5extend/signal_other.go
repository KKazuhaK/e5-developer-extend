//go:build !unix

package main

import "context"

// reloadOnSIGHUP does nothing where there is no SIGHUP.
func reloadOnSIGHUP(context.Context, func()) {}
