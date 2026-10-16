# jrep

**grep for what code *does*.** Ask in plain words, get back the functions
that do it, ranked. Built for coding agents, using TypeSafe's Jev model.

```
$ jrep "retries failed payments" src/
0.94  src/billing/retry.ts:12-80        retryFailedPayment(invoice, attempt)
0.81  src/jobs/payment-worker.ts:40-95  handlePaymentFailure(job)
```

It matches on behaviour, not names, so you needn't know what the code
calls things. Use `rg` for exact strings and names.

## Install

```
export TYPESAFE_API_KEY=...   # fish: set -Ux TYPESAFE_API_KEY ...
make install                  # common-language grammars only, ~20 MB
```

`go install ./cmd/jrep` also works and embeds all 206 grammars (~33 MB).

## Usage

```
jrep "sends the password reset email" src/auth        # one folder, cheapest
jrep "validates webhook signatures" -q "parses webhook payloads" src/
jrep --refine "validates webhook signatures" "checks the HMAC header" src/
jrep -s "exports invoices as CSV" .                   # keyword pre-pass, big repos
jrep --json "retries failed payments" src/
```

| Flag | Does |
|---|---|
| `-q "question"` | Ask another question in the same scan, at almost no extra cost |
| `--refine "earlier"` | Retry a reworded question on the 300 chunks that best matched the earlier one: about a tenth of the cost |
| `-s`, `--keywords 'a\|b'` | Send Jev only chunks containing the question's words, or the given regexp |
| `--threshold 0.6` | Minimum probability to count as a match |
| `--top 10` | Maximum results per question (`0` = no limit) |
| `--tests`, `--docs`, `--no-ignore` | Also search tests, docs, gitignored files |
| `--max-filesize 1M` | Skip larger files, listed on stderr (`0` = no limit) |
| `--json` | One JSON object per match |
| `--no-cache`, `--clear-cache` | Ignore cached answers, or delete this repo's cache |

Run `jrep -h` for every flag and environment variable.

**Exit codes:** `0` matches, `1` nothing above the threshold (the best
candidate is printed), `2` error.

## How it works

1. Lists files, following `.gitignore`. Tests, docs, generated files,
   translations and files over 1 MB are skipped and counted on stderr.
2. Splits each file into function- or block-sized chunks with tree-sitter.
3. Asks Jev about every chunk; a file's chunks share one request.
4. Ranks the chunks for each question, best first.

Answers are cached per repository (worktrees share them), so repeat
searches, and searches at a different `--threshold`, make no new calls.

> [!NOTE]
> Every chunk searched is sent to TypeSafe, and cost grows with the code
> searched. Pass the folder most likely to hold the answer, not the whole
> repo; stderr says how many chunks Jev was asked about.

The search engine is the Go library
[jinter](https://github.com/K1erans/jinter), for use in other projects.
`PLAN.md` explains the design.

## Agent skill

`skills/jrep/SKILL.md` teaches an agent to use jrep well, chiefly to pass
the folders most likely to hold the code instead of scanning the whole
repo. `make install-skill` copies it to `~/.claude/skills/jrep/` for
Claude Code; other agents can read the file as it is.

## Snippet for AGENTS.md / CLAUDE.md

Paste this into an agent's instructions to teach it the basics:

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
