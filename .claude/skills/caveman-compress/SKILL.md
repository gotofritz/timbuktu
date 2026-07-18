---
name: caveman-compress
description: >
  Compress natural language memory files (CLAUDE.md, todos, preferences) into caveman format
  to save input tokens. Preserves all technical substance, code, URLs, and structure.
  Compressed version overwrites the original file. Human-readable backup saved as
  FILE.original.md. Trigger: /caveman:compress FILEPATH or "compress memory file".
---

Compress target file into caveman style. All technical substance survives. Only fluff dies.

## Rules

- Drop: articles (a/an/the), filler (just/really/basically/simply/actually), pleasantries
- Keep: all code blocks unchanged, all URLs unchanged, all technical terms exact
- Fragments OK. Short synonyms. Imperative present tense.
- Preserve all structure: headers, lists, tables, code fences
- Preserve all constraints, rules, and non-obvious context

## Process

1. Read target file
2. Rewrite each section in caveman style — preserve meaning, cut tokens
3. Write compressed version to original path
4. Save original to `FILE.original.md` as backup

## What to compress

- Natural language prose sections
- Docstrings and inline comments (in non-code files)
- Bullet point descriptions

## What never to change

- Code blocks (fenced with ``` or indented)
- Shell commands, file paths, URLs
- Proper nouns, package names, version numbers
- Technical terms that have no shorter equivalent
