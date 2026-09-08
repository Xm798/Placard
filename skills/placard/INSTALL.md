# Installing the Placard skill (for an agent)

You are an AI agent, and the user wants you to install and configure the Placard publishing skill once. Below, `<origin>` is the server address you fetched this file from — Placard has no default server, so that address is whatever the user's self-hosted instance is. Work through four steps in order: install the CLI → sign in → install the skill → verify.

## 1. Install the placard CLI

macOS / Linux:

```bash
curl -fsSL <origin>/install.sh | sh
```

Windows (PowerShell 5.1+, built into Win10/11):

```powershell
irm <origin>/install.ps1 | iex
```

Use the `curl | sh` line on macOS/Linux and the `irm | iex` line on Windows — 5.1 ships with the OS, so nothing else has to be installed first.

The binary comes from the project's GitHub Releases (the `cli/v*` tags). The script installs a single-file binary into a user directory (`~/.local/bin` by default on macOS/Linux, `%LOCALAPPDATA%\placard\bin` on Windows), needs neither sudo nor administrator rights, and verifies the sha256. If it reports that the directory is not on `PATH`, run the line it prints, and tell the user it has to go into their shell config file to survive a new terminal.

When the server itself is unreachable, install straight from GitHub instead: swap the address in the command above for `https://raw.githubusercontent.com/Xm798/Placard/HEAD/installer/install.sh` (or `install.ps1` for PowerShell).

Confirm with `placard --version`. From then on `placard update` upgrades it in place.

## 2. Sign in

```bash
placard login --base <origin>
```

Placard has no built-in default server address, so the first sign-in must name `--base` explicitly; it is remembered afterwards and later commands need not repeat it. To work against several instances at once, pass `--base` per command or set the `PLACARD_URL` environment variable.

This is a device-code flow: the terminal prints an 8-character authorization code and tries to open a browser (honouring `$BROWSER`, and skipped automatically over SSH or without a graphical session). The page it opens already carries the code, so **the user only has to check it and confirm** — you cannot do that part for them. So: pass the code on verbatim, ask them to check that the code on the page matches the terminal and confirm within 180 seconds, then wait for the command to return. It does not matter if no browser opened — pass on the link the terminal printed as well; it works from a browser on any device.

Remind the user to check that the code shown in the browser matches the terminal. If it does not, they should close the page immediately: that check is the anti-phishing step.

On success the credential and the server address are written to `~/.config/placard/config.json` with mode 0600 (XDG-aware: `$XDG_CONFIG_HOME` wins when set), stored per server address. Every later command picks them up on its own — no environment variables to configure.

For a fully non-interactive setting (CI, an automated job), use a PAT instead: ask the user to create an API token on the web app's Settings page (it starts with `pl_` and the plaintext is shown exactly once) and pass it through the `PLACARD_TOKEN` environment variable. Do not use the `--token` flag — it lingers in shell history and in `ps` output. A token is a personal credential: never commit it, and never repeat the plaintext in a later reply.

## 3. Install the skill

Read `<origin>/skill.md` and save its content verbatim as the skill file:

- Claude Code: `~/.claude/skills/placard/SKILL.md`
- Other agents: your own skills / instructions directory

(`~` is the platform's home directory; on Windows read and write it with PowerShell's `Get-Content` / `Set-Content`.)

## 4. Verify

```bash
placard whoami
```

An `authz_id` in the output means it is configured — report the result to the user, who can then publish pages by following the skill.

A user who already has the skill installed can refresh it any time with `curl -sf -o ~/.claude/skills/placard/SKILL.md <origin>/skill.md`; the time to do so is when a publish response's `skill_version` is higher than the `version` in the local frontmatter.
