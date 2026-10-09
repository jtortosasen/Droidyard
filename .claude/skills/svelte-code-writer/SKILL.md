---
name: svelte-code-writer
description: Svelte 5 documentation lookup and code analysis. MUST be used whenever creating, editing or analyzing any Svelte component (.svelte) or Svelte module (.svelte.ts/.svelte.js).
---

## Documentation

`references/svelte-llms-small.txt` in this skill's directory (`.claude/skills/svelte-code-writer/references/svelte-llms-small.txt`) is Svelte's condensed Svelte 5 documentation. Read it when unsure of a rune, block or syntax. It is vendored: do not fetch documentation from the network.

## Svelte autofixer

Analyzes Svelte code with the `@sveltejs/mcp` version pinned in `web/package.json` and reports what to fix. Run it from the repository root on files, never on inline code:

```bash
node web/scripts/autofixer-gate.mjs <path>...
```

```bash
# a component and a module
node web/scripts/autofixer-gate.mjs web/src/App.svelte web/src/lib/counter.svelte.ts
```

- The extension selects the analysis: `.svelte` is a component, `.svelte.ts` and `.svelte.js` are modules.
- The target is always Svelte 5, with the async option off.
- It exits 1 when a file is missing or has an **issue**; that is the same gate the pre-commit hook runs. A **suggestion** does not fail it, but read it and apply it unless you can say why not.

## Workflow

1. **Uncertain about syntax?** Read `references/svelte-llms-small.txt`.
2. **After every write** of a Svelte file, run the autofixer on it; fix every issue and read every suggestion, then run it again until it reports no issues.
3. **Before reporting the step done**, run `pnpm -C web run verify` too: the autofixer and `svelte-check` find different things, and the commit needs both.
