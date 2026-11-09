// Command sandpost is the single Sandpost binary: serve, migrate, admin, and
// the CI helpers wait and extract.
package main

import (
	"context"
	"errors"
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
  admin            account recovery and user tasks (run "sandpost admin help")
  wait             wait for mail in a project (CI helper, talks to the API)
  extract          print a link or one-time code from a message (CI helper)
  help             show this help
`

// env bundles the process dependencies so commands can be tested.
type env struct {
	getenv func(string) string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	// prompt reads a secret from the terminal without echo.
	prompt func(label string) (string, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	e := env{getenv: os.Getenv, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, prompt: terminalPrompt}
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
		err = runAdmin(ctx, e, args[1:])
	case "wait":
		err = runWait(ctx, e, args[1:])
	case "extract":
		err = runExtract(ctx, e, args[1:])
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(e.stdout, usage)
		return exitOK
	default:
		_, _ = fmt.Fprintf(e.stderr, "unknown command %q\n\n%s", args[0], usage)
		return exitUsage
	}
	if err != nil {
		code := exitError
		var ce *codedError
		if errors.As(err, &ce) {
			code = ce.code
		}
		_, _ = fmt.Fprintf(e.stderr, "sandpost %s: %v\n", args[0], err)
		return code
	}
	return exitOK
}
