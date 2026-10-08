# Security policy

Sentinel records video of people's homes and businesses, so security bugs
matter more here than in most projects.

## Reporting a vulnerability

Please **do not open a public issue** for a security problem.

Email **hello@sentvms.com** with "SECURITY" in the subject, or use GitHub's
private "Report a vulnerability" button on the Security tab. Include:

- which app and version (Mac, iPhone, Linux; About screen or `sentinel -version`)
- what an attacker can do, and what access they need first
- steps to reproduce, or a proof of concept

You'll get a reply within a few days. Once a fix ships, you'll be credited in
the release notes unless you'd rather stay anonymous.

## Scope

In scope: the Mac app, the iPhone app, the Linux server and dashboard, the
phone pairing and remote-access path, and how camera passwords and API keys
are stored.

Out of scope: vulnerabilities in the cameras themselves, and in bundled
third-party programs (report those upstream; tell us too if Sentinel should
ship an update).
