// Command sandpost is the single Sandpost binary: serve, migrate and admin.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `Usage: sandpost <command> [arguments]

Commands:
  serve            run the HTTP and SMTP servers
  migrate [dir]    apply database migrations (up, down, status; default up)
  admin            administrative tasks
  help             show this help
`

// env bundles the process dependencies so commands can be tested.
type env struct {
	getenv func(string) string
	stdout io.Writer
	stderr io.Writer
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	e := env{getenv: os.Getenv, stdout: os.Stdout, stderr: os.Stderr}
	os.Exit(run(ctx, e, os.Args[1:]))
}

func run(ctx context.Context, e env, args []string) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(e.stderr, usage)
		return exitUsage
	}
	var err error
	switch args[0] {
	case "serve":
		err = runServe(ctx, e)
	case "migrate":
		err = runMigrate(ctx, e, args[1:])
	case "admin":
		err = runAdmin(e, args[1:])
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(e.stdout, usage)
		return exitOK
	default:
		_, _ = fmt.Fprintf(e.stderr, "unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
	if err != nil {
		_, _ = fmt.Fprintf(e.stderr, "sandpost %s: %v\n", args[0], err)
		return exitError
	}
	return exitOK
}
