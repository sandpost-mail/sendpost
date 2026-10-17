package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// readPassword returns a new password for email. With fromStdin it reads the
// first line of stdin, otherwise it prompts twice on the terminal without
// echo. The password never comes from the command line.
func readPassword(e env, email string, fromStdin bool) (string, error) {
	if fromStdin {
		return readPasswordLine(e.stdin)
	}
	if e.prompt == nil {
		return "", errors.New("no terminal available, use --password-stdin")
	}
	first, err := e.prompt("New password for " + email + ": ")
	if err != nil {
		return "", err
	}
	second, err := e.prompt("Repeat password: ")
	if err != nil {
		return "", err
	}
	if first != second {
		return "", errors.New("the passwords do not match")
	}
	return first, nil
}

func readPasswordLine(r io.Reader) (string, error) {
	if r == nil {
		return "", errors.New("no password on stdin")
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("no password on stdin")
	}
	return line, nil
}

// terminalPrompt writes label to stderr and reads a line from the terminal
// without echo. It refuses to run when stdin is not a terminal.
func terminalPrompt(label string) (string, error) {
	fd := int(os.Stdin.Fd()) //nolint:gosec // file descriptors fit in int
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal, use --password-stdin")
	}
	_, _ = fmt.Fprint(os.Stderr, label)
	secret, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(secret), nil
}
