---
name: jrep
description: Find code by what it does with jrep, scoped to the folders most likely to hold it so each search costs less. Use when looking for where a behaviour is implemented and you don't know the names the code uses; use rg for exact strings and identifiers.
---

# Searching code with jrep

jrep asks TypeSafe's Jev model about every chunk of code under the paths you
give it, so cost grows with the code covered: about 0.7¢ and a second per 400
chunks, and a whole repo can be thousands. Answers are cached, so repeats are
free.

## 1. Pick one folder

Look at the layout (`ls`), then pass the one folder you're sure holds the
code, as deep as you're sure of:

    jrep "retries failed payments" src/billing/

Choose by what the folder is for: payments under `billing/`, handlers under
`api/`. Widen only after a miss, and search the whole repo only when you have
no idea where the code lives. stderr's `asking Jev about N chunks` is the cost.

## 2. Ask well

- Phrase it as what the code does: "sends the password reset email", not
  "where is the email code".
- Tests are skipped unless the question mentions them ("the tests for payment
  retries", "mocks the payment client") or you pass `--tests`.
- To ask other things about the same folder, or the same thing in other
  words, add up to two `-q`. Three questions in one call cost about 40% of
  three calls and answer as well, but each extra still adds about a quarter of
  a scan. More than three can exceed Jev's limit and fail the search.

      jrep "validates webhook signatures" -q "parses webhook payloads" internal/webhooks/

## 3. Read the results

Output is `probability  path:start-end  signature`, best first; read the
printed ranges, not whole files. Exit 0 means matches, 1 none above the
threshold, 2 an error.

## 4. When it misses

Cheapest first:

1. `--threshold 0.3` (default 0.6): free, served from the cache.
2. Narrow the folder if a result points at the right area.
3. Widen: add the next likely folder or step up to the parent. Keep the same
   question so folders already searched come from the cache.
4. Reword and add `--refine "earlier question"`: it asks only the 300 chunks
   that best matched the earlier one, about a tenth of a new scan.

## 5. Before writing new code

Check for code to build on first, scoped to the folder the new code would
live in. `extend` names code to add to, and `create` means write new code; the
jrep-reuse skill covers it.

    jrep reuse "writes search matches out as CSV rows" internal/output/

## What is left out

Docs need `--docs`. Generated files (lockfiles, source maps, minified bundles,
SVGs, `node_modules`) and translation catalogs are skipped unless named as a
path. Files over 1 MB are skipped and listed on stderr.
