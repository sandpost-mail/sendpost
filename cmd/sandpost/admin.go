package main

import (
	"errors"
	"fmt"
)

const adminUsage = `Usage: sandpost admin <task>

Administrative tasks (create-user, reset-password, ...) are not implemented yet. Users and groups are managed through the API (docs/openapi.yaml).
The first admin is created on boot from ADMIN_EMAIL and ADMIN_PASSWORD.
`

// runAdmin is a placeholder for the admin subcommand tree.
func runAdmin(e env, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprint(e.stdout, adminUsage)
		return nil
	}
	return errors.New("task " + fmt.Sprintf("%q", args[0]) + " is not implemented yet")
}
