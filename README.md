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

## Before writing new code

`jrep reuse` says whether to build on existing code or write new code:

```
$ jrep reuse "exports invoices as CSV" src/billing/
extend  0.88  src/billing/export.ts:10-64  exportInvoicesJSON(invoices)

$ jrep reuse "uploads receipts to S3" src/billing/
create  no code to extend above 0.60 (best: 0.12 src/billing/retry.ts:12-80)
```

Claude Haiku describes each chunk in plain words, and Jev is asked about
the descriptions: whether one does what the new code will, or the broader
kind of work it is part of ("exports invoices to an output format" for
CSV). `--code draft.go` checks draft code instead of words. Exit code 0
means extend, 1 create.

Descriptions are cached like answers, so only new or edited code is
described again. Every chunk checked is also sent to Anthropic.

## Install

```
export TYPESAFE_API_KEY=...   # fish: set -Ux TYPESAFE_API_KEY ...
make install                  # common-language grammars only, ~20 MB
```

`jrep reuse` also needs a Claude login, found the way the Anthropic SDK
finds one: `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, or a profile from
`ant auth login`. Without one, it runs Claude Code's `claude` command, which
uses its login, such as a Claude subscription (`claude auth login`). Set
`JREP_CLAUDE_LOGIN` to `api` or `claude` to pick one.

`go install ./cmd/jrep` also works and embeds all 206 grammars (~33 MB).

Run `jrep -h` for flags and environment variables. `PLAN.md` explains the
design.

## Agent skill

`skills/jrep/SKILL.md` teaches an agent to use jrep well, chiefly to pass
the folders most likely to hold the code instead of scanning the whole repo,
since cost scales with the code searched. `skills/jrep-reuse/SKILL.md` has
an agent run `jrep reuse` before writing a new function, and build on the code
it names. `make install-skill` copies both to `~/.claude/skills/` for Claude
Code; other agents can read the files as they are.

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
- Ask one question unless you need more: up to two `-q` for other things
  in the same folder, or rewordings when unsure of the code's words. Three
  in one call cost about 40% of three separate calls and answer as well.
  Don't go past three; with many questions a request can exceed Jev's limit.
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
- Before writing a new function, run `jrep reuse "what it will do" folder/`
  (or `--code draft.go`). `extend` names the code to build on; `create`
  means nothing fits.
- `--json` prints one object per match.
- Files over 1 MB are skipped and listed on stderr; `--max-filesize 0`
  includes them.
```
