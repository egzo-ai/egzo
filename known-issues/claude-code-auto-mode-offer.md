# Claude Code asks "Make auto mode your default permission mode?" on a fresh home

Status: **open, needs a decision** (found 2026-10-04, Claude Code 2.1.289). Not an egzo bug, but egzo's
message injection makes it dangerous.

## What happens

A fresh agent home (`/home/agent` volume) starting Claude Code in bypass mode, after egzo has seeded the
onboarding, workspace trust, API key approval and `skipDangerousModePermissionPrompt`, still shows one
dialog on the first start:

    Make auto mode your default permission mode?
    ❯ Yes, set auto mode as my default permission mode
      No, keep bypass permissions

The TUI reports `SessionStart` before the dialog is answered, so the control sidecar believes the agent
is idle. A queued message is typed in (bracketed paste, then Enter) and the **Enter answers the dialog
with "Yes"**: the agent silently ends up in auto mode, a different permission mode than the one egzo
configured, chosen by nobody.

## What was not done

egzo does not pre-answer this dialog. It is a consent question about a permission mode, and while
looking for the setting that suppresses it, Claude Code's own auto-mode classifier blocked the search
as a bypass of the consent flow. We stopped there. Nothing in `internal/harness/claudecode.go` writes any
auto-mode key.

## Options (for the project owner)

1. Decide that egzo may answer "No, keep bypass permissions" on the user's behalf and seed whatever
   Claude Code stores for that answer (needs the exact key, found deliberately and not by guessing).
2. Do not type into a harness until a human has attached to it at least once, or a first real prompt
   has been submitted (conservative: an unattended fresh agent would wait).
3. Start Claude Code in a way that does not trigger the offer (for example the CLI flag instead of
   the settings file), if one exists. Verify with the spec below.

## The spec

`test_harnesses.py::test_the_real_tui_reports_idle_takes_a_queued_message_and_acknowledges_it[claude-code]`
is marked todo with this reason. It passes once injection into a fresh Claude Code home is safe.
