# jrep

Searches code by what it *does*, using TypeSafe's Jev model. It's built for
coding agents, as a replacement for grep.

```
$ jrep "retries failed payments" src/
0.94  src/billing/retry.ts:12-80        retryFailedPayment(invoice, attempt)
0.81  src/jobs/payment-worker.ts:40-95  handlePaymentFailure(job)
```

Each file is split into function- or block-sized chunks, and Jev is asked
about every chunk. Answers are cached per repo, so repeat searches, and
searches at a different `--threshold`, make no new calls. Every chunk
searched is sent to TypeSafe.

## Install

```
export TYPESAFE_API_KEY=...   # fish: set -Ux TYPESAFE_API_KEY ...
make install                  # common-language grammars only, ~20 MB
```

`go install ./cmd/jrep` also works and embeds all 206 grammars (~33 MB).

Run `jrep -h` for flags and environment variables. `PLAN.md` explains the
design.

## Snippet for AGENTS.md / CLAUDE.md

```markdown
## Searching code

Use `jrep` to find code by behaviour; use `rg` for exact strings and names.

    jrep "retries failed payments" src/
    jrep "validates webhook signatures" -q "parses webhook payloads"

- Phrase the question as what the code does: "sends the password reset
  email", not "where is the email code".
- Output is `probability  path:start-end  signature`, best first. Read the
  ranges it prints rather than whole files.
- Exit code 0 means matches, 1 means none above the threshold, 2 an error.
  On 1 it prints the best candidate: rephrase, or lower `--threshold`
  (cached, so it's free).
- `-q` adds questions to the same scan at no extra cost; prefer one call
  with several `-q` over several calls.
- `-s` sends only chunks containing the question's words (or
  `--keywords 'a|b'`): faster and cheaper on big repos, but misses code
  that uses other words.
- Test files and test data are skipped; `--tests` includes them.
- `--json` prints one object per match.
- Files over 1 MB are skipped and listed on stderr; `--max-filesize 0`
  includes them.
```
