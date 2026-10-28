# Changelog

## Unreleased

- Add versioned Task proposals and private review pages with separate GitHub
  sign-in, an operator approver allowlist, and attributable decision receipts.
- Require approved scope for new work and scope changes. Reuse verified unchanged
  Linear scope across imports and request launches. Existing Task history remains
  intact; unverified consent is unknown.
- Expose proposal creation, revision, and inspection through MCP. Legacy Task
  mutations can return a pending proposal instead of changing work immediately.
- Retain native parent checkpoints while child scope awaits approval. Resume the
  parent after refusal without creating a child Run, even if the proposal is revised
  before the worker resumes it.
- Keep new proposal drafts bound to their selected Project while editing and saving.
