package main

import (
	"context"
	"fmt"

	"github.com/sandpost-mail/sandpost/internal/ciclient"
)

const usageWait = `Usage: sandpost wait [options]

Block until messages matching the filters arrived in a project, then print
their ids, oldest first, one per line (--json prints the server answer).
Mail received before the call counts.

Options:
  --server URL      Sandpost address (default $SANDPOST_URL)
  --project ID      numeric project id (default $SANDPOST_PROJECT)
  --query Q         message search, same syntax as the inbox (subject:welcome)
  --run ID          run id to isolate on (default $SANDPOST_RUN_ID, then
                    $GITHUB_RUN_ID, $CI_PIPELINE_ID, $BUILDKITE_BUILD_ID,
                    $CIRCLE_WORKFLOW_ID; --run= turns it off)
  --count N         messages to wait for, 1 to 50 (default 1)
  --timeout T       seconds or a duration, 0 to 120s (default 30s, 0 checks once)
  --after-id N      only messages with a greater id
  --json            print the JSON answer instead of ids

The API token is read from $SANDPOST_TOKEN (a project token with the "read"
scope is enough). Exit codes: 0 matched, 1 error, 2 usage, 3 timed out.
`

func runWait(ctx context.Context, e env, args []string) error {
	cf := newCIFlags(e, "wait")
	cf.addRun()
	query := cf.fs.String("query", "", "")
	count := cf.fs.Int("count", 1, "")
	timeoutArg := cf.fs.String("timeout", defaultWaitTimeout.String(), "")
	afterID := cf.fs.Int64("after-id", 0, "")
	asJSON := cf.fs.Bool("json", false, "")
	if help, err := cf.parse(e, usageWait, args); help || err != nil {
		return err
	}
	timeout, err := parseTimeout(*timeoutArg)
	if err != nil {
		return err
	}
	run, err := cf.runID(e)
	if err != nil {
		return err
	}
	cl, err := cf.client(e)
	if err != nil {
		return err
	}
	res, raw, err := cl.Wait(ctx, ciclient.WaitParams{Query: *query, Run: run, AfterID: *afterID, Count: *count, Timeout: timeout})
	if err != nil {
		return failure(err)
	}
	if *asJSON {
		_, _ = fmt.Fprint(e.stdout, trimLines(string(raw)))
	} else {
		for _, m := range res.Messages {
			_, _ = fmt.Fprintln(e.stdout, m.ID)
		}
	}
	if res.TimedOut {
		return &codedError{code: exitTimeout, msg: timedOutMessage(timeout, *count, len(res.Messages))}
	}
	return nil
}
