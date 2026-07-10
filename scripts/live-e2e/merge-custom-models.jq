# merge-custom-models.jq
#
# Upsert the live-e2e custom models into a Factory settings object while
# preserving every unrelated custom model the user already configured.
#
#   Input: the existing settings object (or the template, when no valid
#          settings file exists).
#   Arg:   $e2e — the array of live-e2e custom-model entries to upsert.
#   Output: settings with
#           .customModels = (existing models whose `.model` does NOT collide
#                            with an e2e model id and which are not a retired
#                            harness-owned entry) ++ ($e2e)
#
# Properties: preserve-unrelated, upsert-by-model-id, idempotent on rerun
# (a second pass drops the just-added e2e ids and re-appends the same set,
#  yielding an identical customModels set with no duplicates).
# Remove only the exact GPT-5.2 entry written by the previous harness. Matching
# the provider, display name, and loopback proxy URL avoids deleting a
# user-owned model that happens to reuse the same model id.
def retired_live_e2e_codex_model:
  .model == "gpt-5.2-codex"
  and .displayName == "GPT-5.2 Codex (ChatGPT OAuth)"
  and .provider == "openai"
  and ((.baseUrl // "") == "http://127.0.0.1:8787" or (.baseUrl // "") == "http://localhost:8787");

($e2e | map(.model)) as $ids
| .customModels = (
    ((.customModels // []) | map(select(
      ((.model as $m | $ids | index($m)) | not)
      and (retired_live_e2e_codex_model | not)
    )))
    + $e2e
  )
