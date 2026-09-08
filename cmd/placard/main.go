// Command placard is the official Placard CLI: a thin HTTP client over
// /api/* (and the device-code auth endpoints). It never touches the database,
// storage or any server-side package — see the dependency guard in deps_test.go.
package main

import (
	"context"
	"os"
	"os/signal"
	"time"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		// Without a home directory there is no config and no legacy token; the
		// CLI can still work with --token, so degrade instead of dying here.
		home = ""
	}
	// SIGINT cancels the context so the device-code poller can stop between
	// polls; everything else honors it via cmd.Context().
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Two independent descriptors: stdout drives colour, stdin drives whether a
	// human is there to answer a prompt. Conflating them lets a piped `echo y`
	// satisfy a confirmation meant for a person.
	env := NewEnv(os.Stdout, os.Stderr, os.Stdin, home, os.Getenv, time.Now, isTTY(os.Stdout))
	env.StdinTTY = isTTY(os.Stdin)
	if err := RunContext(ctx, env, os.Args[1:]); err != nil {
		env.Out.EmitError(err)
		os.Exit(ExitCode(err))
	}
}
