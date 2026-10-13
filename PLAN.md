# jrep — plan

A command-line tool that searches code by what it *does*, using TypeSafe's Jev model.
It is built for coding agents and is meant to replace grep for them.

```
jrep "retries failed payments" src/
```

## Decisions

| Topic | Decision |
|---|---|
| Form | A single CLI binary. No MCP server, no daemon. |
| Language | Go |
| Search | Every search asks Jev. There is no regex mode. |
| Reuse | `jrep reuse` has Claude Haiku describe each chunk, then asks Jev about the descriptions, not the code. Claude logs in through the Anthropic Go SDK, or Claude Code's `claude` command. |
| Keyword pre-pass | Off by default, so every chunk goes to Jev. `-s` turns it on. |
| Ignored files | `.gitignore` is followed by default. `--no-ignore` turns it off. |
| Test files | Skipped by default (they crowd out the code they test). Included when a question is about tests ("the tests for retries", "mocks the client"), or with `--tests`. |
| Docs | Markdown, reStructuredText and AsciiDoc skipped by default, for the same reason. `--docs` includes them. |
| Generated files | Lockfiles, source maps, minified bundles, SVGs and `node_modules` are always skipped unless named. |
| Results | Line ranges with a probability, best first, short by default. |
| Cache | SQLite (`modernc.org/sqlite`), one database per repository (shared by its worktrees) in the user cache folder. |
| Model | Pinned version (`jev-1.13.0`), overridable with `--model`. |

## Interface

```
jrep [flags] "question" [path...]

  -q "question"          extra question, repeatable; answered in the same Jev calls
  -s                     keyword pre-pass first, fewer chunks sent to Jev
  --keywords 'a|b|c'     regex for the pre-pass (default: words from the question)
  --refine "earlier"     search only the 300 chunks that best matched an earlier question
  --threshold 0.6        minimum probability to count as a match
  --top 10               maximum results per question
  --json                 one JSON object per match
  --no-ignore            also search gitignored files
  --tests                also search test files and test data
  --docs                 also search documentation
  --max-filesize 1M      skip larger files, listed on stderr (0 = no limit)
  --model jev-1.13.0     Jev model version
  --no-cache             don't read cached answers
  --clear-cache          delete this repo's cache and exit

jrep reuse [flags] "what the new code will do" [path...]
jrep reuse [flags] --code draft.go [path...]

  --code FILE            draft code to check, instead of words ("-" reads stdin)
  also --threshold, --no-ignore, --tests, --docs, --max-filesize, --model, --no-cache
```

The API key is read from `TYPESAFE_API_KEY`. `jrep reuse` also needs a Claude login (see Reuse).

Exit codes follow grep:

| Code | Meaning |
|---|---|
| 0 | At least one match |
| 1 | No matches |
| 2 | Error |

## Output

Text, the default:

```
0.94  src/billing/retry.ts:12-80        retryFailedPayment(invoice, attempt)
0.81  src/jobs/payment-worker.ts:40-95  handlePaymentFailure(job)
0.63  src/api/webhooks.ts:110-140       onChargeFailed(event)
```

When nothing passes the cutoff, say so plainly so the agent stops or rephrases:

```
no matches above 0.60 (best: 0.21 src/util/http.ts:5-40)
```

`--json` prints one line per match:

```json
{"question":"retries failed payments","path":"src/billing/retry.ts","start":12,"end":80,"probability":0.94,"signature":"retryFailedPayment(invoice, attempt)"}
```

## Pipeline

```
1. walk      list files, following .gitignore, skipping binaries
2. chunk     split files into functions/blocks of ~10–300 lines, reusing cached layouts
3. pre-pass  only with -s: keep chunks matching the keywords
4. key       sha256(model + question + chunk as sent alone) for every chunk
5. cache     one batched lookup; hits are done
6. ask Jev   misses go out in parallel, within the rate limits
7. save      each answer goes into the cache as it arrives
8. rank      merge, sort by probability, apply threshold and top, print
```

### 1. Walk

- Inside a git repo, use `git ls-files --cached --others --exclude-standard`, so ignore rules match git exactly.
- Outside a repo, walk the folder and parse `.gitignore` files directly.
- Skip binary files (a NUL byte in the first 8 KB). Files are opened in parallel to check.
- Skip generated files: lockfiles, source maps, minified bundles, SVGs and anything in `node_modules` (often missing from `.gitignore`). In one project a lockfile was 70% of the tokens a scan would send. Skip docs unless `--docs`: they describe behaviour too and crowd out code, and were 11% of one large repo's tokens. Always skip translation catalogs (`.po`, `.xliff`, `.arb`, `.strings`, and JSON/YAML/TOML/properties/XML under `locale(s)`, `i18n`, `l10n`, `translation(s)` or `lang(s)` folders): the same strings in every language never say what code does. In miniflux they were 43% of a scan's chunks, and replaying 28 cached questions without them left every target's rank unchanged. Like tests, all of these are counted on stderr, and naming a file or folder searches it.
- Skip files over `--max-filesize` (default 1 MB, `0` = no limit). Files that size are almost always generated: bundles, minified JS, lockfiles, data dumps. One 1 MB file is ~330K tokens, about 3 s of the token rate limit, and its matches are mostly noise.
- Never skip silently: list skipped files on stderr (`skipped 2 files over 1 MB: dist/app.js, …`) so the agent knows and can rerun with `--max-filesize 0`.

### 2. Chunk

- Parse with tree-sitter using [`gotreesitter`](https://github.com/odvcencio/gotreesitter), an existing pure-Go port of the tree-sitter runtime. It needs no cgo or C compiler and loads the standard grammars.
- Split at real function, method, class and type boundaries from the syntax tree.
- For languages without a grammar, or files that fail to parse, fall back to a plain-Go splitter: top-level declarations by indentation, then blank-line boundaries.
- Give up on a runaway parse after 30 s and use the fallback, and cache that split too so it is paid once. A 250 ms limit halved chunking time, but whether a parse beat it depended on load: on t3code 23 files, including Effect.ts (146 ms alone), timed out with every core parsing and not on the next search, so repeat searches re-sent their chunks. The parser's own work limits scale with file size and keep a file's split the same on any machine.
- Merge tiny pieces, and split anything over ~300 lines. Imports, package lines and comments always join the piece after them: on their own they cost a request and never match.
- Cache each file's layout (line ranges, byte offsets, signatures) keyed by its content, name and the splitter's version, so a repeat search parses only changed files. On that repo a fully cached search went from 15.9 s to 1.1 s.
- Keep each chunk well inside Jev's limit of 32K tokens for state plus the longest question. Estimate tokens as bytes ÷ 2.75 to stay on the safe side (measured: ~2.96 bytes per token on Go code sent as JSON). Every request also costs a fixed ~290 tokens, so very small chunks are mostly overhead.
- Record each chunk's signature (the function or class header) for output.
- `gotreesitter` is pre-1.0 and releases often: pin its version, and check how much the bundled grammars add to binary size. If it's large, include only the common languages.

### 3. Keyword pre-pass (`-s`)

- Use `--keywords` if given. Otherwise use the question's words, minus common stopwords.
- Matching is case-insensitive. A chunk is kept if any keyword appears.
- Runs locally.

### 3b. Refining a retry (`--refine`)

- A reworded question costs a full scan, and agents reword when the first answer missed. `--refine "earlier question"` reads the earlier question's cached answers and asks the new one about only its top 300 chunks.
- The agent names the earlier question rather than jrep guessing from similar wording: a retry exists because the first answer was poor, so reusing its answers would repeat the miss. Replaying the v3 challenges, reusing answers got 0 of 4 real retries right; narrowing got 3 of 4, the same as full rescans, at 9% of the cost.
- Chunks with no cached answer for the earlier question are left out: it never saw them, and including them could make the retry a full scan. Refining by a question never asked is an error, not a silent full scan.

### 4–7. Jev calls

Request: `POST https://api.typesafe.ai/v1/systemone`

```json
{
  "model": "jev-1.13.0",
  "state": { "path": "src/billing/retry.ts", "code": "..." },
  "questions": {
    "q0": { "type": "noul", "instructions": "The `code` retries failed payments" }
  }
}
```

- One request per file, up to 24 KB of code. Every request costs ~240 tokens before its code, 40% of a scan's tokens on miniflux, so a file's chunks share one: the state holds `path` and `code_1`, `code_2`, …, and each question names its field ("The `code_2` …"). A lone chunk is sent as `{path, code}`, exactly as before packing. Packing cut a scan's tokens by about a third, and a target scored the same packed among other chunks, in any position, as alone.
- Every question (`q0`, `q1`, …) about a chunk is answered in the same request, so the chunk is paid for once.
- The state has no line numbers. They say nothing about what the code does, and as part of the key they would make an edit near the top of a file re-ask every chunk below it.
- Scans are bound by the request rate, not tokens (a 92K-chunk scan is ~290 s of requests but ~165 s of tokens), and each request carries ~300 fixed tokens, so every chunk not sent saves both time and money.
- Read `answers.qN.noul` (0–1) from the response.
- Rate limits: the docs say 100K tokens/s and 80 requests/s, but a scan at 4x that (320 req/s, 400K tok/s) ran on 2026-10-08 with no 429s. The client-side limiter uses 4x as its ceiling, halves on 429/529 (at most once a second), and adds back a tenth of the ceiling per quiet second. `JREP_MAX_RPS` / `JREP_MAX_TPS` set the ceiling.
- On `429` or `529`, retry with exponential backoff and honour `retry-after`.

## Reuse

`jrep reuse` answers one question before an agent writes new code: build on existing code (`extend`, exit 0), or write new code (`create`, exit 1).

```
extend  0.88  src/billing/export.ts:10-64  exportInvoicesJSON(invoices)
create  no code to extend above 0.60 (best: 0.12 src/billing/retry.ts:12-80)
```

```
1. walk, chunk    as for a search
2. phrase         Haiku turns the plan into two questions: what the new code does (the agent's
                  own words, or Haiku's reading of a --code draft), and the broader kind of
                  work it is part of ("exports invoices as CSV" → "exports invoices to an
                  output format")
3. describe       Haiku describes every chunk not already described, 15 chunks or 64 KB a call
4. ask Jev        both questions about every description, in the same call a search makes: the
                  description goes where the code would ("The `code` …")
5. verdict        the best match for what it does, if above the threshold; else the best for
                  the broader work; else create
```

Measured on jrep's own 134 chunks, with 12 features that exist, 3 that don't and 3 that existing code could be extended to:

- **Descriptions, not code:** Jev ranked a right chunk first for all 12 by description, and the rest fell well behind ("skips lockfiles": 0.95 against 0.17 for the next chunk; by code, 0.89 against 0.94, with the main search loop first). Description and code together did no better than the description alone.
- **Broadening, not "could be extended":** asking whether code "already does, or could be extended to do" a feature put config structs and `main` on top, since almost anything could be extended. Asking about the broader work found the JSON writer for CSV, the flag-value types for duration flags, and the gitignore code for skipping `vendor/`, while email, S3 and keychain stayed under 0.15.
- **The same Jev call as a search:** sending each description in the `code` field gave the same verdict for all 10 reuse requests as a separate `summary` field asked "describes code that …", and for CSV found the JSON writer rather than the search that calls it. The gap to the next chunk was narrower on some ("skips lockfiles": 0.91 against 0.47), and it needs no change to how Jev is called or cached.
- **Pasting code into the question** instead of describing it: "acts similar to this code" put the right function at 0.91 but unrelated flag parsing at 0.81.
- **Speed:** Haiku described the 134 chunks in 20 s, five calls at a time through the `claude` CLI.

**Login:** the Anthropic Go SDK finds the login: `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, a profile named by `ANTHROPIC_PROFILE`, workload identity federation, or the active profile from `ant auth login`. Without one, jrep runs Claude Code's `claude -p` instead, which uses Claude Code's login, such as a Claude subscription; it runs with no tools, MCP servers, settings or skills, in the temp folder, and costs about 1,000 tokens of Claude Code context a call. `JREP_CLAUDE_LOGIN=api` or `claude` picks one. Both send the same model and prompts, so they share cached descriptions. A login is only needed when something isn't cached.

## Cache

```sql
CREATE TABLE answers (
  key        BLOB PRIMARY KEY,   -- sha256(model + question + chunk as sent alone)
  noul       REAL NOT NULL,      -- Jev's probability
  model      TEXT NOT NULL,      -- e.g. "jev-1.13.0"
  last_used  INTEGER NOT NULL    -- unix time
);

CREATE TABLE chunks (
  key        BLOB PRIMARY KEY,   -- sha256(splitter version + options + file name + content)
  layout     BLOB NOT NULL,      -- each chunk's lines, byte offset, length and signature
  last_used  INTEGER NOT NULL
);

CREATE TABLE descriptions (
  key        BLOB PRIMARY KEY,   -- sha256(Claude model + prompt + path + code), or of a reuse plan
  text       TEXT NOT NULL,      -- Claude's description, or a plan's two questions as JSON
  last_used  INTEGER NOT NULL
);
```

- **Location:** `<user cache dir>/jrep/<sha256 of repo>.db`. On macOS that's `~/Library/Caches/jrep/`. The repo is the main checkout's folder, so every worktree of a repository shares one database (keys use repo-relative paths, so that's safe); outside git it is the search path.
- **Key rule:** hash exactly what Jev is asked about one chunk, as if sent alone, even when it shared a request with the rest of its file. An edited chunk, renamed file, different question or new model version makes a new key, so stale answers can't come back. Code that only moved within its file keeps its key, and editing one function re-asks only that function.
- **Question normalisation:** lowercase, collapse whitespace and trim before hashing.
- **Store the probability,** so changing `--threshold` needs no new Jev calls.
- **Several processes at once:** WAL mode, a busy timeout and `INSERT OR IGNORE`. Switching a new file to WAL fails at once, without the busy timeout, while another process holds it, so opening retries.
- **Interrupted runs:** answers are saved as they arrive, so a killed run keeps its progress.
- **Cleanup:** at most once a day, delete rows unused for 30 days and answers from other model versions.
- **Descriptions** are keyed by the prompt as well as the model and chunk, so editing the prompt describes everything again. Jev's answers about a description are keyed by the description, so they are kept apart from answers about code.
- **Interface:** `Get(keys) map[key]noul`, `Put(key, noul)`, plus `GetChunks(keys)` and a batched `PutChunks(layouts)` for chunk layouts, and `GetDescriptions` / `PutDescriptions` for descriptions, so SQLite can be swapped out in one file.

### Why SQLite

| Option | Problem |
|---|---|
| bbolt | A writing process locks out every other process, so a second agent waits for the whole scan. |
| Badger / Pebble | Only one process may open the database at all. |
| One file per answer | 1M answers become 1M files, about 4 GB on disk for about 8 MB of data. |
| Append-only file per question | Locking, compaction and crash safety would all be hand-written. |
| JSON file | Two writers overwrite each other. |

SQLite costs about 5–10 MB of binary size. Pure Go keeps builds free of a C compiler.

## Project layout

```
cmd/jrep/main.go        flags, wiring, exit codes
internal/walk/          file listing, gitignore, binary detection
internal/chunk/         splitting files into chunks
internal/prefilter/     -s keyword pre-pass
internal/jev/           API client, rate limiter, retries (behind an interface)
internal/describe/      Claude's descriptions and reuse questions (behind an interface)
internal/cache/         SQLite cache (behind an interface)
internal/search/        the pipeline: key → cache → Jev → rank
internal/output/        text and JSON printing
```

## Testing

- Unit tests for walking, chunking, the pre-pass, cache keys and ranking.
- A fake Jev client gives deterministic scores, so the whole pipeline is testable offline.
- Concurrency test: several processes sharing one cache database.
- A real-API smoke test that runs only when `TYPESAFE_API_KEY` is set, and ones for Claude when `ANTHROPIC_API_KEY` or `JREP_CLAUDE_SMOKE=1` is set.

## Build order

1. Go module, CLI flags, exit codes.
2. Walk and gitignore handling.
3. Chunking: `gotreesitter` first, with the plain-Go splitter as fallback.
4. Fake Jev client, search pipeline, ranking, output. **Usable end to end offline.**
5. SQLite cache.
6. `-s` keyword pre-pass.
7. Real Jev client with rate limiting and retries.
8. Measure real latency and throughput, then tune chunk size and parallelism.
9. Write the `AGENTS.md` / `CLAUDE.md` snippet that tells agents how to use jrep.

## Open questions and risks

- **Throughput:** measured on miniflux (658 files): a full scan took 25.6 s at the documented limits and 8.3 s at 4x. Requests take ~235 ms (p90 ~300 ms) regardless of rate. Repeat searches are fast once cached.
- **Rate limits** can change without notice.
- **Chunk size vs accuracy:** TypeSafe notes accuracy shifts as the state grows. Tune after measuring.
- **Question wording:** how a search is phrased for Noul (e.g. "The `code` …") affects accuracy. Test a few templates.
- **Privacy:** every chunk searched is sent to TypeSafe. With `jrep reuse`, every chunk checked is also sent to Anthropic.
- **Reuse on big repos:** a first `jrep reuse` describes every chunk it covers, so scope it to the folder the new code would live in. Haiku's rate limits on large repos are unmeasured.
