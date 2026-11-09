package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sandpost-mail/sandpost/internal/ciclient"
)

const usageExtract = `Usage: sandpost extract [options]

Print a verification link or one-time code from a message. Without --message
it first waits for a matching message, like "sandpost wait", and reads the
oldest match (the newest with --latest).

Options:
  --server URL          Sandpost address (default $SANDPOST_URL)
  --project ID          numeric project id (default $SANDPOST_PROJECT)
  --message ID          read this message, do not wait
  --print code|link     what to print (default: link with --link-contains and
                        no --code-pattern, otherwise code)
  --link-contains S     keep links containing S (case insensitive)
  --code-pattern RE     RE2 pattern for the code, group 1 when present
  --all                 print every value, one per line (default: the first)
  --json                print {"message_id","value","links","codes"}
  waiting: --query Q, --run ID, --after-id N, --timeout T, --latest
           (same meaning as "sandpost wait"; not allowed with --message)

The API token is read from $SANDPOST_TOKEN. Only the value goes to stdout, so
CODE=$(sandpost extract) works. Exit codes: 0 found, 1 error, 2 usage,
3 no message before the timeout, 4 message without such a link or code.
`

// extractOutput is what --json prints.
type extractOutput struct {
	MessageID int64           `json:"message_id"`
	Value     *string         `json:"value"`
	Links     []string        `json:"links"`
	Codes     []ciclient.Code `json:"codes"`
}

func runExtract(ctx context.Context, e env, args []string) error {
	cf := newCIFlags(e, "extract")
	cf.addRun()
	messageID := cf.fs.Int64("message", 0, "")
	what := cf.fs.String("print", "", "")
	linkContains := cf.fs.String("link-contains", "", "")
	codePattern := cf.fs.String("code-pattern", "", "")
	all := cf.fs.Bool("all", false, "")
	asJSON := cf.fs.Bool("json", false, "")
	query := cf.fs.String("query", "", "")
	afterID := cf.fs.Int64("after-id", 0, "")
	timeoutArg := cf.fs.String("timeout", defaultWaitTimeout.String(), "")
	latest := cf.fs.Bool("latest", false, "")
	if help, err := cf.parse(e, usageExtract, args); help || err != nil {
		return err
	}
	kind, err := chooseKind(*what, *linkContains, *codePattern)
	if err != nil {
		return err
	}
	if cf.set("message") {
		if *messageID < 1 {
			return usageError("--message must be a positive message id")
		}
		for _, name := range []string{"query", "run", "after-id", "timeout", "latest"} {
			if cf.set(name) {
				return usageError("--%s only applies when waiting, it can not be combined with --message", name)
			}
		}
	}
	timeout, err := parseTimeout(*timeoutArg)
	if err != nil {
		return err
	}
	cl, err := cf.client(e)
	if err != nil {
		return err
	}
	if !cf.set("message") {
		id, err := waitForMessage(ctx, e, cf, cl, ciclient.WaitParams{Query: *query, AfterID: *afterID, Count: 1, Timeout: timeout}, *latest)
		if err != nil {
			return err
		}
		*messageID = id
	}
	ex, _, err := cl.Extract(ctx, *messageID, ciclient.ExtractParams{LinkContains: *linkContains, CodePattern: *codePattern})
	if err != nil {
		return failure(err)
	}
	values := selectValues(ex, kind)
	if *asJSON {
		out := extractOutput{MessageID: ex.MessageID, Links: nonNil(ex.Links), Codes: nonNilCodes(ex.Codes)}
		if len(values) > 0 {
			out.Value = &values[0]
		}
		b, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(e.stdout, string(b))
	} else if len(values) > 0 {
		if !*all {
			values = values[:1]
		}
		_, _ = fmt.Fprintln(e.stdout, strings.Join(values, "\n"))
	}
	if len(values) == 0 {
		return &codedError{code: exitNotFound, msg: fmt.Sprintf("message %d has no %s", ex.MessageID, kind)}
	}
	return nil
}

func chooseKind(what, linkContains, codePattern string) (string, error) {
	switch what {
	case "code", "link":
		return what, nil
	case "":
		if linkContains != "" && codePattern == "" {
			return "link", nil
		}
		return "code", nil
	default:
		return "", usageError("--print must be code or link")
	}
}

func selectValues(ex ciclient.Extraction, kind string) []string {
	if kind == "link" {
		return ex.Links
	}
	out := make([]string, 0, len(ex.Codes))
	for _, c := range ex.Codes {
		out = append(out, c.Value)
	}
	return out
}

func waitForMessage(ctx context.Context, e env, cf *ciFlags, cl *ciclient.Client, p ciclient.WaitParams, latest bool) (int64, error) {
	run, err := cf.runID(e)
	if err != nil {
		return 0, err
	}
	p.Run = run
	res, _, err := cl.Wait(ctx, p)
	if err != nil {
		return 0, failure(err)
	}
	if res.TimedOut || len(res.Messages) == 0 {
		return 0, &codedError{code: exitTimeout, msg: timedOutMessage(p.Timeout, 1, len(res.Messages))}
	}
	if latest {
		return res.Messages[len(res.Messages)-1].ID, nil
	}
	return res.Messages[0].ID, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilCodes(c []ciclient.Code) []ciclient.Code {
	if c == nil {
		return []ciclient.Code{}
	}
	return c
}
