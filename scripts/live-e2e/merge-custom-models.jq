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
#                            with an e2e model id) ++ ($e2e)
#
# Properties: preserve-unrelated, upsert-by-model-id, idempotent on rerun
# (a second pass drops the just-added e2e ids and re-appends the same set,
#  yielding an identical customModels set with no duplicates).
($e2e | map(.model)) as $ids
| .customModels = (
    ((.customModels // []) | map(select((.model as $m | $ids | index($m)) | not)))
    + $e2e
  )
