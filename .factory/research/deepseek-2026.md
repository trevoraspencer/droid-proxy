# DeepSeek 2026 Research Notes

DeepSeek docs reviewed during planning indicate newer model names such as `deepseek-v4-flash` and `deepseek-v4-pro`, with legacy names like `deepseek-chat` and `deepseek-reasoner` treated as compatibility aliases/deprecated. Workers should update user-facing examples to avoid presenting legacy names as the current recommendation unless they are explicitly labeled legacy/compatibility.

Automated validation must still use fake local upstreams only and must not call DeepSeek.
