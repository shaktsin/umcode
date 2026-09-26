# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | :white_check_mark: |

This project is in active development. Only the latest release receives security updates.

## Reporting a Vulnerability

**⚠️ DO NOT open public GitHub issues for security vulnerabilities.**

Please report security issues privately via:
- Email: [Create a private security advisory if available]
- Direct message to maintainers

### What to Include
- Clear description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if known)
- Your contact information for follow-up

### Response Timeline
- **Acknowledgment**: Within 48 hours
- **Initial Assessment**: Within 1 week
- **Fix Development**: 2-4 weeks (severity-dependent)
- **Public Disclosure**: After fix release + user update window

## Security Model

- **Project sandbox.** File and shell tools resolve every path against the project root, symlinks included, and refuse anything outside it. A write outside a project fails; it is never offered as an approval.
- **Approvals.** Sensitive tool actions require approval in the app or CLI. Unanswered approvals count as denied after `policy.approval_timeout_minutes`.
- **Credentials.** Provider API keys are stored in the macOS Keychain (on Linux, `~/.umcode/secrets.json`, mode 0600). Shell commands and skill scripts run without the engine's API keys in their environment.
- **Network.** With a project's `network` switch off, commands that obviously reach out (`curl`, `git push`, `npm install`, ...) are refused.
- **Local access only.** The engine listens on a Unix socket (mode 0600) and on `127.0.0.1` with a per-start bearer token; browser clients from other origins are refused.

See [GO_ENGINE.md](GO_ENGINE.md) for details.
