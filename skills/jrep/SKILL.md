---
name: jrep
description: Find code by what it does with jrep, scoped to the folders most likely to hold it so each search costs less. Use when looking for where a behaviour is implemented and you don't know the names the code uses; use rg for exact strings and identifiers.
---

# Searching code with jrep

jrep asks TypeSafe's Jev model about every chunk of code under the paths you
give it, so a search costs money and time in proportion to how much code it
covers: about 1¢ and a second per 400 chunks, and a whole repo can be
thousands. Answers are cached, so repeating a question is free.

## 1. Pick the folders first

Don't search the whole repo by default. Look at the layout, then pass the
narrowest folder you're confident holds the code:

    ls                       # and ls a level down where it helps
    jrep "retries failed payments" src/billing/

- Choose by what the folder is for: payments under `billing/`, storage
  under `storage/`, request handlers under `api/` or `ui/`.
- Several folders are fine: `jrep "question" src/billing/ src/http/`.
- When unsure between two levels, take the parent folder. Search the whole
  repo only when you have no idea where the code lives.
- stderr says `asking Jev about N chunks`: that is the cost of the search.
  Nothing is printed when every answer was cached.

## 2. Ask well

- Phrase the question as what the code does: "sends the password reset
  email", not "where is the email code".
- Say what kind of code you want. Looking for tests? Say so: "the tests for
  payment retries", "mocks the payment client". Test files are skipped
  unless a question mentions tests (test, testing, mock, fixture, …) or you
  pass `--tests`; stderr says when they were searched.
- Ask everything you need in one call: `-q` adds questions to the same scan
  at almost no extra cost.

      jrep "validates webhook signatures" -q "parses webhook payloads" internal/

## 3. Read the results

- Output is `probability  path:start-end  signature`, best first. Read the
  ranges it prints rather than whole files.
- Exit code 0 means matches, 1 means none above the threshold, 2 an error.

## 4. When it misses

In this order, cheapest first:

1. Lower the threshold: `--threshold 0.3`. It's served from the cache, so
   it's free, and the right code is often just under the default 0.6.
2. Narrow the folder if a result points at the right area.
3. Reword the question and add `--refine "your earlier question"`: it asks
   the new wording about only the 300 chunks that best matched the earlier
   one, about a tenth of the cost of a new scan.

## What is left out

Tests are skipped unless a question is about tests or you add `--tests`;
docs are skipped unless you add `--docs`. Generated
files (lockfiles, source maps, minified bundles, SVGs, `node_modules`) and
translation catalogs are skipped unless you name them as a path. Files over
1 MB are skipped and listed on stderr.
