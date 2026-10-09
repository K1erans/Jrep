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
| Keyword pre-pass | Off by default, so every chunk goes to Jev. `-s` turns it on. |
| Ignored files | `.gitignore` is followed by default. `--no-ignore` turns it off. |
| Test files | Skipped by default (they crowd out the code they test). `--tests` includes them. |
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
```

The API key is read from `TYPESAFE_API_KEY`.

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
4. key       sha256(model + question + chunk as sent) for every chunk
5. cache     one batched lookup; hits are done
6. ask Jev   misses go out in parallel, within the rate limits
7. save      each answer goes into the cache as it arrives
8. rank      merge, sort by probability, apply threshold and top, print
```

### 1. Walk

- Inside a git repo, use `git ls-files --cached --others --exclude-standard`, so ignore rules match git exactly.
- Outside a repo, walk the folder and parse `.gitignore` files directly.
- Skip binary files (a NUL byte in the first 8 KB). Files are opened in parallel to check.
- Skip generated files: lockfiles, source maps, minified bundles, SVGs and anything in `node_modules` (often missing from `.gitignore`). In one project a lockfile was 70% of the tokens a scan would send. Skip docs unless `--docs`: they describe behaviour too and crowd out code, and were 11% of one large repo's tokens. Like tests, both are counted on stderr, and naming a file or folder searches it.
- Skip files over `--max-filesize` (default 1 MB, `0` = no limit). Files that size are almost always generated: bundles, minified JS, lockfiles, data dumps. One 1 MB file is ~330K tokens, about 3 s of the token rate limit, and its matches are mostly noise.
- Never skip silently: list skipped files on stderr (`skipped 2 files over 1 MB: dist/app.js, …`) so the agent knows and can rerun with `--max-filesize 0`.

### 2. Chunk

- Parse with tree-sitter using [`gotreesitter`](https://github.com/odvcencio/gotreesitter), an existing pure-Go port of the tree-sitter runtime. It needs no cgo or C compiler and loads the standard grammars.
- Split at real function, method, class and type boundaries from the syntax tree.
- For languages without a grammar, or files that fail to parse, fall back to a plain-Go splitter: top-level declarations by indentation, then blank-line boundaries.
- Give up on a parse after 250 ms and use the fallback. A few files take seconds (one 44 KB Swift file took 9.7 s) and usually end in a syntax error anyway; on a 17.5K-file repo this halved chunking time.
- Merge tiny pieces, and split anything over ~300 lines. Imports, package lines and comments always join the piece after them: on their own they cost a request and never match.
- Cache each file's layout (line ranges, byte offsets, signatures) keyed by its content, name and the splitter's version, so a repeat search parses only changed files. On that repo a fully cached search went from 15.9 s to 1.1 s.
- Keep each chunk well inside Jev's limit of 32K tokens for state plus the longest question. Estimate tokens as bytes ÷ 2.75 to stay on the safe side (measured: ~2.96 bytes per token on Go code sent as JSON). Every request also costs a fixed ~290 tokens, so very small chunks are mostly overhead.
- Record each chunk's signature (the function or class header) for output.
- `gotreesitter` is pre-1.0 and releases often: pin its version, and check how much the bundled grammars add to binary size. If it's large, include only the common languages.

### 3. Keyword pre-pass (`-s`)

- Use `--keywords` if given. Otherwise use the question's words, minus common stopwords.
- Matching is case-insensitive. A chunk is kept if any keyword appears.
- Runs locally.

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

- One request per chunk. Every question (`q0`, `q1`, …) is answered in that one request, so the chunk is paid for once.
- The state has no line numbers. They say nothing about what the code does, and as part of the key they would make an edit near the top of a file re-ask every chunk below it.
- Scans are bound by the request rate, not tokens (a 92K-chunk scan is ~290 s of requests but ~165 s of tokens), and each request carries ~300 fixed tokens, so every chunk not sent saves both time and money.
- Read `answers.qN.noul` (0–1) from the response.
- Rate limits: the docs say 100K tokens/s and 80 requests/s, but a scan at 4x that (320 req/s, 400K tok/s) ran on 2026-10-08 with no 429s. The client-side limiter uses 4x as its ceiling, halves on 429/529 (at most once a second), and adds back a tenth of the ceiling per quiet second. `JREP_MAX_RPS` / `JREP_MAX_TPS` set the ceiling.
- On `429` or `529`, retry with exponential backoff and honour `retry-after`.

## Cache

```sql
CREATE TABLE answers (
  key        BLOB PRIMARY KEY,   -- sha256(model + question + chunk as sent)
  noul       REAL NOT NULL,      -- Jev's probability
  model      TEXT NOT NULL,      -- e.g. "jev-1.13.0"
  last_used  INTEGER NOT NULL    -- unix time
);

CREATE TABLE chunks (
  key        BLOB PRIMARY KEY,   -- sha256(splitter version + options + file name + content)
  layout     BLOB NOT NULL,      -- each chunk's lines, byte offset, length and signature
  last_used  INTEGER NOT NULL
);
```

- **Location:** `<user cache dir>/jrep/<sha256 of repo>.db`. On macOS that's `~/Library/Caches/jrep/`. The repo is the main checkout's folder, so every worktree of a repository shares one database (keys use repo-relative paths, so that's safe); outside git it is the search path.
- **Key rule:** hash exactly what is sent to Jev. An edited chunk, renamed file, different question or new model version makes a new key, so stale answers can't come back. Code that only moved within its file keeps its key.
- **Question normalisation:** lowercase, collapse whitespace and trim before hashing.
- **Store the probability,** so changing `--threshold` needs no new Jev calls.
- **Several processes at once:** WAL mode, a busy timeout and `INSERT OR IGNORE`. Switching a new file to WAL fails at once, without the busy timeout, while another process holds it, so opening retries.
- **Interrupted runs:** answers are saved as they arrive, so a killed run keeps its progress.
- **Cleanup:** at most once a day, delete rows unused for 30 days and answers from other model versions.
- **Interface:** `Get(keys) map[key]noul`, `Put(key, noul)`, plus `GetChunks(keys)` and a batched `PutChunks(layouts)` for chunk layouts, so SQLite can be swapped out in one file.

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
internal/cache/         SQLite cache (behind an interface)
internal/search/        the pipeline: key → cache → Jev → rank
internal/output/        text and JSON printing
```

## Testing

- Unit tests for walking, chunking, the pre-pass, cache keys and ranking.
- A fake Jev client gives deterministic scores, so the whole pipeline is testable offline.
- Concurrency test: several processes sharing one cache database.
- A real-API smoke test that runs only when `TYPESAFE_API_KEY` is set.

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
- **Privacy:** every chunk searched is sent to TypeSafe.
