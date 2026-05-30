#!/usr/bin/env zsh

set -euo pipefail

SCRIPT_DIR="${0:A:h}"
source "$SCRIPT_DIR/lib.sh"

cd "$LIVE_E2E_REPO_ROOT"
load_local_env

EVIDENCE_MD="$LIVE_E2E_RUN_DIR/factory-manual-evidence.md"
FACTORY_DIR="$LIVE_E2E_RUN_DIR/factory"

typeset -a rows
rows=(
  "ChatGPT/Codex OAuth|gpt-5.2-codex"
  "xAI OAuth (Grok Build)|grok-build-0.1"
  "xAI Grok 4.3 OAuth|grok-4.3"
  "Z.AI GLM coding|glm-5.1"
  "Xiaomi MiMo|mimo-v2.5-pro"
  "Fireworks|${FIREWORKS_MODEL}"
  "DeepSeek|deepseek-v4-flash"
)

{
  print -r -- "# Factory Droid Manual Evidence"
  print -r -- ""
  print -r -- "- Run id: $LIVE_E2E_RUN_ID"
  print -r -- "- Proxy base URL: http://127.0.0.1:8787"
  print -r -- "- Settings template has already pointed custom models at droid-proxy when the final runner reaches this step."
  print -r -- ""
  print -r -- "For each model, select the Factory Droid custom model and run both prompts."
  print -r -- ""
} > "$EVIDENCE_MD"

for row in "${rows[@]}"; do
  provider="${row%%|*}"
  alias="${row#*|}"
  model_dir="$FACTORY_DIR/$alias"
  result_file="$model_dir/result.txt"
  mkdir -p "$model_dir"

  {
    print -r -- "## $provider"
    print -r -- ""
    print -r -- "- Alias: \`$alias\`"
    print -r -- "- Expected file: \`$result_file\`"
    print -r -- ""
    print -r -- "Text prompt:"
    print -r -- ""
    print -r -- '```text'
    print -r -- "Reply exactly: droid-proxy-ok"
    print -r -- '```'
    print -r -- ""
    print -r -- "File/tool prompt:"
    print -r -- ""
    print -r -- '```text'
    print -r -- "Create $result_file with the exact text \"$alias ok\", then read it back and report the file contents."
    print -r -- '```'
    print -r -- ""
    print -r -- "- [ ] Text prompt returned \`droid-proxy-ok\`."
    print -r -- "- [ ] File exists and contains \`$alias ok\`."
    print -r -- "- [ ] Proxy log contains requests for \`$alias\` and no old proxy URL."
    print -r -- ""
  } >> "$EVIDENCE_MD"

  append_result "$provider" "$alias" "factory text" "MANUAL" "complete $EVIDENCE_MD"
  append_result "$provider" "$alias" "factory file task" "MANUAL" "expected $result_file"
done

info "Factory Droid manual evidence checklist written to $EVIDENCE_MD"
