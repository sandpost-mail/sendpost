package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/sandpost-mail/sandpost/internal/audit"
	"github.com/sandpost-mail/sandpost/internal/config"
	"github.com/sandpost-mail/sandpost/internal/store"
	"github.com/sandpost-mail/sandpost/internal/users"
)

// auditSourceCLI is recorded as "actor" in the audit detail of CLI changes.
const auditSourceCLI = "cli"

const adminUsage = `Usage: sandpost admin <task> [arguments]

Account recovery tasks. Run them on the server: they use DATABASE_URL and
SECRET_KEY like "sandpost serve" and change the database directly.

Tasks:
  list-users                                list users
  create-admin [--name N] <email>           create an administrator
  reset-password <email>                    set a new password, end the user's sessions
  unlock <email>                            enable a disabled user
  promote <email>                           make a user an administrator

Passwords are read from a prompt without echo, or from the first line of
stdin with --password-stdin (create-admin and reset-password). They are never
read from arguments. Every change is written to the audit log as "actor": "cli".
`

// adminDeps is what a task needs once the database is open.
type adminDeps struct {
	e   env
	svc *users.Service
}

func runAdmin(ctx context.Context, e env, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprint(e.stdout, adminUsage)
		return nil
	}
	rest := args[1:]
	switch args[0] {
	case "list-users":
		return adminListUsers(ctx, e, rest)
	case "create-admin":
		return adminCreateAdmin(ctx, e, rest)
	case "reset-password":
		return adminResetPassword(ctx, e, rest)
	case "unlock":
		return adminUnlock(ctx, e, rest)
	case "promote":
		return adminPromote(ctx, e, rest)
	default:
		return fmt.Errorf("unknown admin task %q (see \"sandpost admin help\")", args[0])
	}
}

// withAdmin loads the configuration, connects, and runs fn with a context whose
// audit entries are attributed to the CLI.
func withAdmin(ctx context.Context, e env, fn func(ctx context.Context, d adminDeps) error) error {
	cfg, err := config.Load(e.getenv)
	if err != nil {
		return err
	}
	pool, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return fn(audit.WithSource(ctx, auditSourceCLI), adminDeps{e: e, svc: users.NewService(pool)})
}

// taskFlags parses the flags of one task. Flags may come before or after the
// positional arguments. It returns the positional arguments, which must number
// exactly positional.
func taskFlags(fs *flag.FlagSet, usage string, positional int, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%w\n%s", err, usage)
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(pos) != positional {
		return nil, errors.New(usage)
	}
	return pos, nil
}
