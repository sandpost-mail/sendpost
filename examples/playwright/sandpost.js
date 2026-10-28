// Thin wrapper around the sandpost CLI. The CLI reads SANDPOST_URL,
// SANDPOST_PROJECT, SANDPOST_TOKEN and SANDPOST_RUN_ID from the environment.
const { execFile } = require("node:child_process");

// extract waits for a matching message (up to `timeout` seconds) and resolves
// with the first code, or the first link when `link` is set. It rejects with
// the CLI's own message on a timeout (exit 3) or when nothing was found (exit 4).
function extract({ query = "", link = "", codePattern = "", timeout = 30 } = {}) {
  const args = ["extract", "--timeout", String(timeout)];
  if (query) args.push("--query", query);
  if (link) args.push("--link-contains", link);
  if (codePattern) args.push("--code-pattern", codePattern);
  return new Promise((resolve, reject) => {
    execFile("sandpost", args, { env: process.env }, (err, stdout, stderr) => {
      if (err) return reject(new Error(`sandpost extract failed (exit ${err.code}): ${stderr.trim()}`));
      resolve(stdout.trim());
    });
  });
}

module.exports = { extract };
