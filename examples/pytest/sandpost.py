"""Thin wrapper around the sandpost CLI.

The CLI reads SANDPOST_URL, SANDPOST_PROJECT and SANDPOST_TOKEN from the
environment. The run id is passed explicitly so a test (or a fixture) can
choose it.
"""
import subprocess

TIMEOUT_EXIT = 3
NOT_FOUND_EXIT = 4


class MailError(AssertionError):
    """No matching mail before the timeout (exit 3) or no such code or link (exit 4)."""


def extract(run_id, query="", link="", code_pattern="", timeout=30):
    """Wait for a message of this run and return its first code (or link when `link` is set)."""
    args = ["sandpost", "extract", "--run", run_id, "--timeout", str(timeout)]
    if query:
        args += ["--query", query]
    if link:
        args += ["--link-contains", link]
    if code_pattern:
        args += ["--code-pattern", code_pattern]
    proc = subprocess.run(args, capture_output=True, text=True, timeout=timeout + 30)
    if proc.returncode != 0:
        raise MailError(f"sandpost extract failed (exit {proc.returncode}): {proc.stderr.strip()}")
    return proc.stdout.strip()
