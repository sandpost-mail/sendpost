package main

import (
	"context"
	"flag"
	"fmt"
	"text/tabwriter"

	"github.com/sandpost-mail/sandpost/internal/users"
)

const ( //nolint:gosec // usage strings, not credentials
	usageCreateAdmin = "usage: sandpost admin create-admin [--name NAME] [--password-stdin] <email>"
	usageReset       = "usage: sandpost admin reset-password [--password-stdin] <email>"
	usageUnlock      = "usage: sandpost admin unlock <email>"
	usagePromote     = "usage: sandpost admin promote <email>"
	usageListUsers   = "usage: sandpost admin list-users"
)

func adminListUsers(ctx context.Context, e env, args []string) error {
	if _, err := taskFlags(flag.NewFlagSet("list-users", flag.ContinueOnError), usageListUsers, 0, args); err != nil {
		return err
	}
	return withAdmin(ctx, e, func(ctx context.Context, d adminDeps) error {
		all, err := d.svc.List(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "ID\tEMAIL\tADMIN\tDISABLED\tMUST CHANGE PASSWORD")
		for _, u := range all {
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%t\t%t\t%t\n", u.ID, u.Email, u.IsAdmin, u.DisabledAt != nil, u.MustChangePassword)
		}
		return tw.Flush()
	})
}

func adminCreateAdmin(ctx context.Context, e env, args []string) error {
	fs := flag.NewFlagSet("create-admin", flag.ContinueOnError)
	name := fs.String("name", "", "display name (default: the part of the email before @)")
	stdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	pos, err := taskFlags(fs, usageCreateAdmin, 1, args)
	if err != nil {
		return err
	}
	email := pos[0]
	return withAdmin(ctx, e, func(ctx context.Context, d adminDeps) error {
		password, err := readPassword(e, email, *stdin)
		if err != nil {
			return err
		}
		u, err := d.svc.Create(ctx, users.CreateInput{Email: email, Name: *name, Password: password, IsAdmin: true})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(e.stdout, "created administrator %s (id %d), password change required at first login\n", u.Email, u.ID)
		return nil
	})
}

func adminResetPassword(ctx context.Context, e env, args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	stdin := fs.Bool("password-stdin", false, "read the password from the first line of stdin")
	pos, err := taskFlags(fs, usageReset, 1, args)
	if err != nil {
		return err
	}
	return withAdmin(ctx, e, func(ctx context.Context, d adminDeps) error {
		u, err := d.svc.GetByEmail(ctx, pos[0])
		if err != nil {
			return err
		}
		password, err := readPassword(e, u.Email, *stdin)
		if err != nil {
			return err
		}
		if err := d.svc.ResetPassword(ctx, u.ID, password); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(e.stdout, "password of %s reset, sessions ended, password change required at next login\n", u.Email)
		return nil
	})
}

func adminUnlock(ctx context.Context, e env, args []string) error {
	pos, err := taskFlags(flag.NewFlagSet("unlock", flag.ContinueOnError), usageUnlock, 1, args)
	if err != nil {
		return err
	}
	return withAdmin(ctx, e, func(ctx context.Context, d adminDeps) error {
		u, err := d.svc.GetByEmail(ctx, pos[0])
		if err != nil {
			return err
		}
		if u.DisabledAt == nil {
			_, _ = fmt.Fprintf(e.stdout, "%s is already enabled\n", u.Email)
			return nil
		}
		enabled := false
		if _, err := d.svc.Update(ctx, u.ID, users.UpdateInput{Disabled: &enabled}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(e.stdout, "%s enabled\n", u.Email)
		return nil
	})
}

func adminPromote(ctx context.Context, e env, args []string) error {
	pos, err := taskFlags(flag.NewFlagSet("promote", flag.ContinueOnError), usagePromote, 1, args)
	if err != nil {
		return err
	}
	return withAdmin(ctx, e, func(ctx context.Context, d adminDeps) error {
		u, err := d.svc.GetByEmail(ctx, pos[0])
		if err != nil {
			return err
		}
		if u.IsAdmin {
			_, _ = fmt.Fprintf(e.stdout, "%s is already an administrator\n", u.Email)
			return nil
		}
		admin := true
		if _, err := d.svc.Update(ctx, u.ID, users.UpdateInput{IsAdmin: &admin}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(e.stdout, "%s is now an administrator\n", u.Email)
		if u.DisabledAt != nil {
			_, _ = fmt.Fprintf(e.stdout, "note: %s is disabled, run \"sandpost admin unlock %s\"\n", u.Email, u.Email)
		}
		return nil
	})
}
