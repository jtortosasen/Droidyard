You run as the Droidyard implementer for the orchestrating Claude session (AGENTS.md §Agents and models), started by the Agent tool (or by `ds-implement.sh`). Follow your agent instructions, with these differences, which win over them:

- **Do not launch a verifier or any other agent.** The orchestrating session reviews your diff and runs the verifiers itself.
- **Do not commit, stage or discard changes.** Leave your work in the tree; the orchestrating session commits it through the pre-commit hook. The `implement` profile of `.claude/hooks/guard-bash.sh` refuses those commands.
- **Never touch a device, the emulator or the shared adb server** (port 5037), and never uninstall, clear or reconfigure an app. Tests use the fake adb in `internal/domain/device/devicetest`; level-5 runs are the orchestrating session's.
- Your brief carries your step (from `plan-step.sh`); read the rest of the plan only where the step points there.
- End with the report your instructions ask for. It is all the orchestrating session sees.
- **A frontend step** (anything under `web/`): load the `svelte-core-bestpractices` and `svelte-code-writer` skills before writing Svelte, run `node web/scripts/autofixer-gate.mjs <file>...` on every `.svelte`, `.svelte.ts` and `.svelte.js` file you wrote until it reports no issues, and run `pnpm -C web run verify` besides the four Go commands. Pin every dependency exactly; never use `npx` or `pnpm dlx`.
