package main

import (
	"errors"
	"fmt"
)

const adminUsage = `Usage: sandpost admin <task>

Administrative tasks (create-user, reset-password, ...) arrive with M1.8 and M2.
`

// runAdmin is a placeholder for the admin subcommand tree.
func runAdmin(e env, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprint(e.stdout, adminUsage)
		return nil
	}
	return errors.New("task " + fmt.Sprintf("%q", args[0]) + " is not implemented yet")
}
