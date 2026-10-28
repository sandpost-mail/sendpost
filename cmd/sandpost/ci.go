package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sandpost-mail/sandpost/internal/ciclient"
)

// Exit codes of the CI commands (wait, extract). 0, 1 and 2 are shared with
// the other commands. A script can tell "no mail yet" from "broken setup".
const (
	exitTimeout  = 3 // wait: not enough matching messages before the timeout
	exitNotFound = 4 // extract: the message has no link or code of the asked kind
)

const (
	envURL     = "SANDPOST_URL"
	envProject = "SANDPOST_PROJECT"
	envToken   = "SANDPOST_TOKEN"
	envRunID   = "SANDPOST_RUN_ID"
)

// runIDFallbacks are read, in order, when neither --run nor SANDPOST_RUN_ID is
// set: the run (or pipeline) number the common CI systems provide.
var runIDFallbacks = []string{"GITHUB_RUN_ID", "CI_PIPELINE_ID", "BUILDKITE_BUILD_ID", "CIRCLE_WORKFLOW_ID"}

const defaultWaitTimeout = 30 * time.Second

// codedError makes run exit with a specific code. An empty message prints nothing.
type codedError struct {
	code int
	msg  string
}

func (e *codedError) Error() string { return e.msg }

func usageError(format string, a ...any) error {
	return &codedError{code: exitUsage, msg: fmt.Sprintf(format, a...)}
}

// ciFlags are the options shared by wait and extract.
type ciFlags struct {
	fs      *flag.FlagSet
	server  string
	project string
	run     string
}

func newCIFlags(e env, name string) *ciFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	c := &ciFlags{fs: fs}
	fs.StringVar(&c.server, "server", e.getenv(envURL), "Sandpost address (default $"+envURL+")")
	fs.StringVar(&c.project, "project", e.getenv(envProject), "numeric project id (default $"+envProject+")")
	return c
}

// addRun registers --run, used by wait and by extract when it waits.
func (c *ciFlags) addRun() {
	c.fs.StringVar(&c.run, "run", "", "run id to isolate on (default $"+envRunID+", then the CI run id; --run= disables)")
}

// parse parses args. help is true when -h was given and the usage was printed.
func (c *ciFlags) parse(e env, usage string, args []string) (help bool, err error) {
	if err := c.fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprint(e.stdout, usage)
			return true, nil
		}
		return false, usageError("%v\n\n%s", err, usage)
	}
	if c.fs.NArg() > 0 {
		return false, usageError("unexpected argument %q\n\n%s", c.fs.Arg(0), usage)
	}
	return false, nil
}

// set reports whether the flag was given on the command line.
func (c *ciFlags) set(name string) bool {
	found := false
	c.fs.Visit(func(f *flag.Flag) { found = found || f.Name == name })
	return found
}

// runID resolves the run id: the flag (an explicit empty value turns the
// filter off), SANDPOST_RUN_ID, then the CI variables.
func (c *ciFlags) runID(e env) (string, error) {
	id := c.run
	if !c.set("run") {
		id = e.getenv(envRunID)
		for _, name := range runIDFallbacks {
			if id != "" {
				break
			}
			id = e.getenv(name)
		}
	}
	if id == "" {
		return "", nil
	}
	if err := ciclient.ValidateRunID(id); err != nil {
		return "", usageError("%v", err)
	}
	return id, nil
}

// client builds the API client. The token only comes from the environment,
// like every secret of this binary (no flag, so it stays out of ps and logs).
func (c *ciFlags) client(e env) (*ciclient.Client, error) {
	cl, err := ciclient.New(c.server, c.project, e.getenv(envToken))
	if err != nil {
		return nil, usageError("%v", err)
	}
	return cl, nil
}

// parseTimeout accepts whole seconds ("30") or a duration ("45s", "2m").
func parseTimeout(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, usageError("invalid --timeout %q: use seconds (30) or a duration (45s, 2m)", s)
	}
	return d, nil
}

// failure maps a client error to an exit error: bad arguments are usage
// errors, everything else a plain failure.
func failure(err error) error {
	if errors.Is(err, ciclient.ErrInvalid) {
		return usageError("%v", err)
	}
	return err
}

func timedOutMessage(timeout time.Duration, want, got int) string {
	return fmt.Sprintf("timed out after %s: %d of %d message(s) matched", timeout, got, want)
}

func trimLines(s string) string { return strings.TrimRight(s, "\n") + "\n" }
