---
name: jrep-reuse
description: Before writing a new function, method, type or helper, run `jrep reuse` to check whether existing code already does it or could be extended to, so you build on that code instead of writing a copy. Use once you know what the new code will do; skip it for edits inside code you've already found.
---

# Checking for code to build on with jrep reuse

`jrep reuse` answers one question before you write new code: **extend**
existing code, or **create** new code. Claude describes each chunk of code in
plain words, then TypeSafe's Jev model is asked which description does what
your new code will, or the broader kind of work it is part of.

## 1. When to run it

Once you know what the new code will do, and before you write it: a new
function, method, type, helper or file. Skip it for changes inside code you
have already found, and for a line or two of glue.

## 2. Say what the new code does, or pass a draft

    jrep reuse "parses durations like 5s or 2m from a flag" cmd/
    jrep reuse --code draft.go internal/output/
    jrep reuse --code - internal/output/ < sketch.go

- In words, phrase it as what the code does, as for a search: "retries
  failed webhook deliveries", not "webhook retry helper".
- If you have sketched the code already, pass it with `--code` (`-` reads
  stdin). stderr says how Claude read it: `jrep: the draft …`.

## 3. Pick the folder

Pass the folder the new code would live in, plus any folder a shared helper
would be in (`internal/util/`, `lib/`). Don't check the whole repo by default.

The first run on a folder has Claude describe every chunk, about 20 s for 170
chunks. After that, descriptions come from the cache, and only edited code is
described again. Tests are skipped unless the feature is about tests or you
add `--tests`.

## 4. Read the answer

    extend  0.88  src/billing/export.ts:10-64  exportInvoicesJSON(invoices)
    create  no code to extend above 0.60 (best: 0.12 src/billing/retry.ts:12-80)

- `extend` (exit 0): read that range before writing anything. It either
  already does the job, so use it, or does the broader kind of work (stderr:
  `jrep: also asking for code that …`), so add to it: a parameter, a case,
  or a sibling function beside it using the same helpers. Don't write a
  parallel version.
- `create` (exit 1): nothing fits, so write the new code. If the `best` it
  names looks related, read it first; it may still be where the code goes.
- Exit 2 is an error, usually a missing login (see below).

`extend` is a lead, not an order. If the code it names doesn't fit once
you've read it, write new code.

## 5. When the answer looks wrong

1. `--threshold 0.4` (default 0.6): free, answered from the cache.
2. Reword: name the behaviour, not where it is used ("formats a byte count as
   KB or MB", not "size display for the CLI").
3. Widen to the next likely folder.

## Logins

Only needed for what isn't cached yet:

- TypeSafe: `TYPESAFE_API_KEY`.
- Claude: `ANTHROPIC_API_KEY` (or `ANTHROPIC_AUTH_TOKEN`, or an
  `ant auth login` profile). Without one, jrep uses Claude Code's own login
  (`claude auth login`, such as a Claude subscription).
  `JREP_CLAUDE_LOGIN=api` or `claude` picks one.

The code checked is sent to Anthropic as well as TypeSafe.
