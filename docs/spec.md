# Spec: jrep, behaviour search for coding agents

Synthesised from the jrep handoff (2026-10-08). The handoff defers detail to the plan, and the handoff's "user decisions not to reopen" are binding. Triage label: `ready-for-agent`.

## Problem Statement

Coding agents find code with grep. That only works when they already know the words the code uses. When an agent asks "where do we retry failed payments?", grep needs a guess like `retry|backoff|attempt`. A wrong guess means missed code, or many files read in full to make up for it. The agent needs a search that matches on what code *does*, and answers like grep does: short, line-precise, best first, and clear when nothing matched so the agent can stop or rephrase.

## Solution

`jrep` is a single command-line binary with no server, daemon or MCP. It asks TypeSafe's Jev model about every chunk of code and prints line ranges with a probability, best first:

```
jrep "retries failed payments" src/
0.94  src/billing/retry.ts:12-80        retryFailedPayment(invoice, attempt)
```

Every search goes through Jev, and there is no regex mode. Files are split into function- or block-sized chunks and asked about in parallel, backing off whenever TypeSafe signals its rate limit. Answers are cached per repo, so repeat searches, and the same search at a different threshold, cost nothing. An optional keyword pre-pass (`-s`) cuts cost on big repos. Without `-s`, nothing is filtered.

## User Stories

1. As a coding agent, I want to search code by describing its behaviour in plain English, so that I find it without guessing identifiers.
2. As a coding agent, I want results as `probability  path:start-end  signature`, so that I can read just the relevant lines.
3. As a coding agent, I want results ordered best first, so that the first line is the most likely answer.
4. As a coding agent, I want results capped at a small number by default (`--top 10`), so that output stays short.
5. As a coding agent, I want a minimum probability (`--threshold 0.6`), so that weak candidates don't look like answers.
6. As a coding agent, I want a plain one-line message when nothing passes the threshold, with the best candidate's score and location, so that I know to stop or rephrase.
7. As a coding agent, I want grep-style exit codes (0 match, 1 no match, 2 error), so that scripts and tool loops can branch on the result.
8. As a coding agent, I want to ask several questions in one scan (`-q`, repeatable), so that each chunk is sent and paid for once.
9. As a coding agent, I want each question's results labelled when I ask several, so that I can tell them apart.
10. As a coding agent, I want `--json` with one object per match, so that I can parse results reliably.
11. As a coding agent, I want JSON stdout to hold only match objects, so that no-match messages don't break parsing.
12. As a coding agent, I want flags allowed before or after the question and paths, so that I don't trip on argument order.
13. As a coding agent, I want `--` to end flag parsing, so that I can search for a question that starts with a dash.
14. As a coding agent, I want to pass one or more paths, defaulting to the current folder, so that I can scope a search.
15. As a coding agent, I want a named file searched even if it is gitignored, so that an explicit request is honoured.
16. As a developer, I want `.gitignore` followed by default, so that vendored, built and generated files are not searched or paid for.
17. As a developer, I want ignore rules inside a git repo to match git exactly, so that jrep and git agree on which files count.
18. As a developer, I want `.gitignore` files honoured outside a git repo too, so that unversioned folders behave the same way.
19. As a developer, I want `--no-ignore`, so that I can search ignored files on purpose.
19a. As a coding agent, I want test files and test data skipped unless I pass `--tests` or my question is about tests, so that the code under test isn't crowded out of the results, but a search for tests finds them.
19b. As a coding agent, I want docs skipped unless I pass `--docs`, so that prose describing a feature doesn't crowd out its code.
19c. As a developer, I want lockfiles, source maps, minified bundles, SVGs, `node_modules` and translation catalogs skipped unless named, so that I don't pay for files that never answer a question.
20. As a developer, I want binary files skipped silently, so that they never reach Jev.
21. As a coding agent, I want files over `--max-filesize` (default 1 MB) skipped and listed on stderr, so that nothing is skipped without my knowing.
22. As a coding agent, I want the skip message to say how to include those files (`--max-filesize 0`), so that I can rerun at once.
23. As a coding agent, I want each result's range to follow real function, method, class and type boundaries, so that a range holds one complete unit.
24. As a coding agent, I want doc comments kept with the code they describe, so that the context Jev and I see is complete.
25. As a coding agent, I want each result to carry the function or class header as its signature, so that I can judge relevance without opening the file.
26. As a coding agent, I want files in languages without a grammar, or that fail to parse, still searched, so that coverage never depends on parser support.
27. As a developer, I want chunks kept to roughly 10–300 lines and well inside Jev's 32K-token limit, so that requests are valid, answers stay accurate and most results point at a single function.
28. As a developer, I want `-s` to send only chunks containing the question's words, so that big repos cost less.
29. As a developer, I want `--keywords 'a|b|c'` to set the `-s` keywords, so that I control what the pre-pass matches.
30. As a developer, I want every chunk sent to Jev when `-s` is off, so that recall never silently depends on wording.
31. As a coding agent, I want to be told when `-s` kept nothing, and which keywords it used, so that I don't mistake an empty pre-pass for an empty answer.
32. As a developer, I want answers cached on disk per repo, so that repeat searches make no API calls.
33. As a developer, I want the cache keyed on exactly what was sent (model, question, chunk), so that edited code, renamed files, new questions and new model versions never return stale answers.
34. As a developer, I want questions that differ only in case or spacing to share cache entries, so that trivial rewording costs nothing.
35. As a developer, I want probabilities stored rather than yes/no verdicts, so that changing `--threshold` needs no new calls.
36. As a developer, I want each answer saved as it arrives, so that a killed or failed run keeps its progress.
37. As a developer running several agents at once, I want them to share one cache safely, so that none of them blocks or corrupts it.
38. As a developer, I want old and other-version cache rows cleaned up at most once a day, so that the cache doesn't grow without limit.
39. As a developer, I want `--no-cache` to skip reading cached answers, so that I can force fresh ones.
40. As a developer, I want `--clear-cache` to delete this repo's cache and exit, so that I can reset it.
41. As a developer, I want the model pinned (`jev-1.13.0`) and overridable with `--model`, so that results don't drift when TypeSafe moves an alias.
42. As a developer, I want the API key read from `TYPESAFE_API_KEY` and never passed as a flag, so that it stays out of shell history and logs.
43. As a developer, I want searches answered entirely from cache to work without a key, so that a missing key only matters when Jev is needed.
44. As a developer, I want requests and tokens per second limited on the client, with a configurable ceiling that backs off on 429s and recovers, so that scans run as fast as TypeSafe allows without hammering it when limits change.
45. As a developer, I want 429 and 529 replies retried with exponential backoff that honours `retry-after`, so that brief overloads don't fail a scan.
46. As a developer, I want clear errors for a bad key or a rejected request, so that I can fix setup quickly.
47. As a developer, I want Ctrl-C to stop a search promptly and keep the answers saved so far, so that interrupting costs nothing extra.
48. As a coding agent, I want a short AGENTS.md / CLAUDE.md snippet explaining how and when to use jrep, so that I use it well with no other instructions.
49. As a coding agent, I want to retry a reworded question over only the chunks that best matched my earlier question, so that rephrasing after a miss costs a tenth of a new scan.
50. As a coding agent, I want a skill that tells me to pass the folders most likely to hold the code, and a stderr line saying how many chunks Jev was asked about, so that I search no more code than I need to.

## Implementation Decisions

- **Form:** one Go binary, CLI only. An MCP server and a daemon were explicitly rejected.
- **Modules:** the CLI (flags, wiring, exit codes); walk (file listing, ignore rules, binary and size checks); chunk (splitting); prefilter (the `-s` pass); jev (API client, rate limiter, retries, behind a client interface); cache (behind a `Get(keys)` / `Put(key, noul)` interface plus `GetChunks` / `PutChunks` for chunk layouts, so the store can be swapped in one place); search (key → cache → Jev → rank); output (text and JSON).
- **Walking:** inside git, the file list comes from git (tracked plus untracked-but-not-ignored). Outside git, `.gitignore` files are parsed. A file is binary if its first 8 KB contain a NUL byte. `--max-filesize` takes sizes like `1M`, and `0` means no limit.
- **Chunking:** uses the existing pure-Go tree-sitter port `gotreesitter` at a pinned version. Do not write a port. Split at top-level syntax nodes and open up oversized nodes into their children. Merge pieces under 10 lines and split anything over ~300 lines. Estimate tokens as bytes ÷ 2.75 and keep chunks well inside 32K tokens. Fall back to a plain splitter (unindented lines after blank lines) when there is no grammar, parsing fails or a runaway parse passes 30 s (a safety net only: a tighter clock makes the same file split differently on a busy machine). Imports, package lines and comments join the piece after them. Each file's layout is cached by content, so repeat searches parse only changed files. Check the grammars' binary-size cost; if it is large, ship only common languages.
- **Jev request:** `POST /v1/systemone` with the pinned model, a state of `{path, code}` (no line numbers, so moved code keeps its cached answers) and one Noul question per search question (`q0`, `q1`, …), all in a single request per chunk. Read `answers.qN.noul`. The question is phrased as a statement about the state's `code` field.
- **Cache:** SQLite via a pure-Go driver. This was approved after comparison with bbolt, Badger, plain files and JSON. There is one database per repository in the user cache folder, named by a hash of the main checkout's folder, so worktrees share it (outside git, the search path). The answers table holds key, probability, model and last-used time; a chunks table holds each file's layout by content hash. WAL mode, a busy timeout and insert-or-ignore handle concurrent processes. Cleanup runs daily and deletes rows unused for 30 days and rows from other model versions.
- **Key:** sha256 over model + normalised question + state as sent. Normalising means lowercasing, collapsing white space and trimming.
- **Output:** aligned text by default; one JSON object per line with `--json`. The no-match line reads `no matches above <threshold> (best: <p> <path:range>)`.

## Testing Decisions

- A good test drives a public seam and checks observable behaviour: output text, exit codes, the requests Jev receives, and what a later run reads back. It never reaches into internals.
- **Seams**, highest first: the CLI run (arguments in; stdout, stderr and exit code out), with an injected Jev client and cache; then the search pipeline, the cache interface, the Jev client against a local HTTP server, file walking, chunking and the pre-pass.
- A fake Jev client gives deterministic scores, so the whole pipeline runs offline.
- A concurrency test runs several real processes against one cache database.
- A real-API smoke test runs only when `TYPESAFE_API_KEY` is set.

## Out of Scope

- A regex or literal mode. Every search goes through Jev.
- An MCP server, daemon, watch mode or index server.
- Non-Noul question types (Choice, Score).
- Writing a tree-sitter port.
- Measuring real latency and throughput and tuning chunk size and parallelism (build step 8). This needs the API key and comes next.

## Further Notes

- Every chunk searched is sent to TypeSafe. That is the privacy trade-off.
- TypeSafe says rate limits "can change without notice". Measured on 2026-10-08 against miniflux: ~235 ms per call regardless of rate; no 429s even with 12 scans at once (peak 987 req/s); each request costs ~290 tokens plus one per ~2.96 bytes. Accuracy against chunk size is still unmeasured beyond the 12-task agent runs.
- Question wording affects accuracy, so templates should be tested once the key is available. TypeSafe's agent skill may help.
