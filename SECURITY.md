# Security Policy

## Supported versions

`tbuk` is pre-1.0 and released from `main`. Security fixes land on the latest
release only; there is no back-porting to older tags. Always run the most
recent release.

| Version        | Supported |
|----------------|-----------|
| Latest release | ✅        |
| Older releases | ❌        |

## Reporting a vulnerability

Please report vulnerabilities **privately** — do not open a public issue for a
security problem.

Preferred channel: [GitHub private vulnerability
reporting](https://github.com/gotofritz/timbuktu/security/advisories/new)
("Report a vulnerability" under the repository's **Security** tab). This keeps
the report confidential until a fix is available and lets us coordinate a
disclosure with you.

When reporting, please include:

- affected version or commit,
- steps to reproduce (a minimal input file or command line is ideal),
- the impact you observed.

`tbuk` parses untrusted documents (PDF, HTML, Markdown, plain text) and ships
standalone binaries, so parser crashes, path-traversal on ingest, and
terminal-escape injection in rendered output are all in scope.

## What to expect

We aim to acknowledge a report within a few days, agree on a fix and
disclosure timeline with the reporter, and credit reporters in the release
notes unless anonymity is requested.
