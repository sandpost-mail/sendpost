package main

import (
	"context"
	"fmt"

	"github.com/sandpost-mail/sandpost/internal/config"
	"github.com/sandpost-mail/sandpost/internal/store"
)

const usageMigrate = `Usage: sandpost migrate [up|down|status]

  up       apply every pending migration (default)
  down     roll back the most recent migration
  status   list migrations and whether they are applied
`

func runMigrate(ctx context.Context, e env, args []string) error {
	dir := "up"
	switch len(args) {
	case 0:
	case 1:
		dir = args[0]
	default:
		return usageError("too many arguments\n\n%s", usageMigrate)
	}
	if isHelp(dir) {
		_, _ = fmt.Fprint(e.stdout, usageMigrate)
		return errHelpShown
	}
	if dir != "up" && dir != "down" && dir != "status" {
		return usageError("unknown migrate direction %q (want up, down or status)", dir)
	}
	cfg, err := config.Load(e.getenv)
	if err != nil {
		return err
	}
	pool, err := store.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch dir {
	case "down":
		if err := store.MigrateDown(ctx, pool); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(e.stdout, "rolled back one migration")
	case "status":
		sts, err := store.MigrationStatuses(ctx, pool)
		if err != nil {
			return err
		}
		for _, s := range sts {
			state := "pending"
			if s.Applied {
				state = "applied"
			}
			_, _ = fmt.Fprintf(e.stdout, "%05d %-8s %s\n", s.Version, state, s.Source)
		}
	default:
		n, err := store.MigrateUp(ctx, pool)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(e.stdout, "applied %d migration(s)\n", n)
	}
	return nil
}
