#!/usr/bin/env bash
# Runs "sandpost wait" or "sandpost extract" for action.yml. Everything arrives
# in INPUT_* environment variables (and SANDPOST_*), nothing is interpolated.
# Writes the step outputs and exits with the CLI's exit code (3 timeout,
# 4 nothing found), so a timeout fails the step.
set -uo pipefail

args=("$INPUT_COMMAND")
add() { [ -n "$2" ] && args+=("$1" "$2"); return 0; }

case "$INPUT_COMMAND" in
wait)
  add --count "$INPUT_COUNT"
  ;;
extract)
  add --message "$INPUT_MESSAGE_ID"
  add --print "$INPUT_PRINT"
  add --link-contains "$INPUT_LINK_CONTAINS"
  add --code-pattern "$INPUT_CODE_PATTERN"
  [ "$INPUT_LATEST" = "true" ] && args+=(--latest)
  ;;
*)
  echo "sandpost action: command must be install, wait or extract, got '$INPUT_COMMAND'" >&2
  exit 2
  ;;
esac
add --query "$INPUT_QUERY"
add --after-id "$INPUT_AFTER_ID"
add --run "$INPUT_RUN_ID"
add --timeout "$INPUT_TIMEOUT"
# With --message the CLI refuses the waiting options: drop them.
if [ "$INPUT_COMMAND" = extract ] && [ -n "$INPUT_MESSAGE_ID" ]; then
  filtered=()
  skip=0
  for a in "${args[@]}"; do
    if [ "$skip" = 1 ]; then skip=0; continue; fi
    case "$a" in --query | --after-id | --run | --timeout) skip=1 ;; *) filtered+=("$a") ;; esac
  done
  args=("${filtered[@]}")
fi

out="$(mktemp)"
sandpost "${args[@]}" --json >"$out"
rc=$?

delim="EOF_$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
if [ -s "$out" ] && command -v jq >/dev/null 2>&1; then
  if [ "$INPUT_COMMAND" = wait ]; then
    {
      echo "message-id=$(jq -r '.messages[0].id // empty' "$out")"
      echo "message-ids=$(jq -r '[.messages[].id] | join(" ")' "$out")"
    } >>"$GITHUB_OUTPUT"
  else
    value="$(jq -r '.value // empty' "$out")"
    # A one-time code is a secret for the log: mask it before anything prints it.
    case "$value" in "" | http://* | https://* | mailto:*) ;; *) echo "::add-mask::$value" ;;
    esac
    {
      echo "message-id=$(jq -r '.message_id // empty' "$out")"
      echo "value<<$delim"
      echo "$value"
      echo "$delim"
    } >>"$GITHUB_OUTPUT"
  fi
  {
    echo "json<<$delim"
    cat "$out"
    echo "$delim"
  } >>"$GITHUB_OUTPUT"
fi
rm -f "$out"
exit "$rc"
