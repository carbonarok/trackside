# Security

Please report security problems privately, not in a public issue. Use
GitHub's [private vulnerability reporting](https://github.com/carbonarok/trackside/security/advisories/new)
for this repository.

Include what you found, how to reproduce it, and what an attacker could do
with it. You'll get a reply as soon as possible.

## What trackside protects

- **`API_KEY`** is a single shared key for a small group of users, not
  per-user accounts. It's compared in constant time, and `/healthz` is the
  only path without it. Serve it over HTTPS, since Basic and Bearer
  credentials are otherwise sent in clear text.
- **Feed credentials, the APNs key and inbox keys** are read from the
  environment and never sent to clients.
- **Live Activity registrations** hold device push tokens. They're removed
  when a journey ends, when Apple reports the token invalid, or after six
  hours without a push.

Without `API_KEY`, everything trackside serves is public by design.
