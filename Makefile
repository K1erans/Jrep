# Embeds only common tree-sitter grammars: ~20 MB instead of ~33 MB with all
# 206. Files in other languages use the plain fallback splitter. A plain
# `go build ./cmd/jrep` still works and embeds every grammar.
GRAMMARS := go python javascript typescript tsx java c cpp c_sharp rust ruby \
	php kotlin swift scala bash lua elixir haskell ocaml dart zig html css scss \
	json yaml toml sql hcl vue svelte markdown
TAGS := grammar_subset $(addprefix grammar_subset_,$(GRAMMARS))

.PHONY: build install install-skill test
build:
	go build -tags '$(TAGS)' -o jrep ./cmd/jrep

install:
	go install -tags '$(TAGS)' ./cmd/jrep

# The agent skill: how to search with jrep, scoped to the right folders.
install-skill:
	mkdir -p $(HOME)/.claude/skills/jrep
	cp skills/jrep/SKILL.md $(HOME)/.claude/skills/jrep/SKILL.md

test:
	go test ./...
