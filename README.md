# jrep

Searches code by what it *does*, using TypeSafe's Jev model. It's built for
coding agents, as a replacement for grep.

```
$ jrep "retries failed payments" src/
0.94  src/billing/retry.ts:12-80        retryFailedPayment(invoice, attempt)
0.81  src/jobs/payment-worker.ts:40-95  handlePaymentFailure(job)
```

Each file is split into function- or block-sized chunks, and Jev is asked
about every chunk. Answers are cached per repository (worktrees share
them), so repeat searches, and searches at a different `--threshold`, make
no new calls. Every chunk searched is sent to TypeSafe.

## Install

```
export TYPESAFE_API_KEY=...   # fish: set -Ux TYPESAFE_API_KEY ...
make install                  # common-language grammars only, ~20 MB
```

`go install ./cmd/jrep` also works and embeds all 206 grammars (~33 MB).

Run `jrep -h` for flags and environment variables. `PLAN.md` explains the
design.

The search engine is a Go library,
[jinter](https://github.com/K1erans/jinter), for use in other projects;
jrep is the command line around it.

## Agent skill

`skills/jrep/SKILL.md` teaches an agent to use jrep well, chiefly to pass
the folders most likely to hold the code instead of scanning the whole repo,
since cost scales with the code searched. `make install-skill` copies it to
`~/.claude/skills/jrep/` for Claude Code; other agents can read the file as
it is.

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
- Pass the one folder most likely to hold the code, as deep as you're
  sure of (`ls` first); add another or step up to the parent only after a
  miss. Cost scales with how much code is searched, and stderr says how
  many chunks Jev was asked about.
- When a search misses and you reword the question, add
  `--refine "your earlier question"`: it searches only the 300 chunks that
  best matched the earlier one, about a tenth of the cost of a new scan.
- `-s` sends only chunks containing the question's words (or
  `--keywords 'a|b'`): faster and cheaper on big repos, but misses code
  that uses other words.
- Test files and test data are skipped unless the question is about tests
  ("the tests for payment retries") or you pass `--tests`. Docs
  (Markdown etc.) are skipped; `--docs` includes them. Lockfiles, source
  maps, minified files, SVGs, `node_modules` and translation catalogs are
  skipped unless named.
- `--json` prints one object per match.
- Files over 1 MB are skipped and listed on stderr; `--max-filesize 0`
  includes them.
```
