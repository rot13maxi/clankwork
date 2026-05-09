# Security

Clankwork is a local automation and control-plane tool. It is intended to be
run against repositories and agent runtimes you trust.

Do not treat Clankwork as a security sandbox. Workflow templates, repository
verification commands, runtime commands, and agent actions can execute local
processes against your filesystem and network.

## Reporting vulnerabilities

Please report security issues privately through GitHub's
[private vulnerability reporting](https://github.com/rot13maxi/clankwork/security/advisories/new)
for this repository.

If private reporting is not available, open a minimal public issue requesting
a private channel rather than disclosing details in the open issue.

## Current security posture

- Per-task git worktrees reduce accidental cross-task interference, but they
  are not a sandbox. Workers can still touch anything the daemon process can
  reach on the host.
- ACP permission policies (`worktree`, `trusted`, `manual`, `deny`) can
  constrain some runtime actions. Treat them as defense-in-depth, not as a
  trust boundary against a hostile agent runtime.
- Artifact hashes provide integrity metadata, not cryptographic attestation
  of identity. There is no signing or supply-chain provenance verification.
- Repository verification, lint, and typecheck commands run as configured;
  do not register repos with `--auto-push` or untrusted verify commands you
  have not reviewed.
- Multi-user authorization is not implemented. The daemon assumes a single
  trusted local operator on a Unix socket.

## Hardening recommendations

- Run Clankwork under a dedicated user account with limited filesystem and
  network access.
- Keep agent runtimes (Claude Code, Codex, Pi, the ACP adapter) up to date.
- Avoid running untrusted workflow templates, role markdown, or third-party
  ACP adapters.
- Review generated changes (and failed tasks) before enabling `auto_push`
  on a repo.
