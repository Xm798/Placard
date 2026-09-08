---
name: placard
version: 9
description: Publish self-contained HTML pages to Placard and hand back a shareable internal link. Use when the user asks to publish or share a page to Placard, or wants an internal link for an HTML report, dashboard, prototype, diff walkthrough, or any output easier to look at than to read as terminal text.
---

# Placard — publish HTML pages as internal links

Placard turns one self-contained HTML file into a company-internal link (`<base>/s/<id>`): viewers pass the login wall, and the page renders in a sandboxed iframe.

Everything below runs through the `placard` CLI, which holds the credential for you — NEVER print or embed a token.

## Setup check

Before the first publish in a session, confirm the CLI is installed and logged in:

```bash
placard whoami
```

- Prints an `authz_id` → ready, go publish.
- `command not found` → the CLI is not installed. Ask the user for their Placard server address, fetch `<base>/install.md` from it, and walk the user through it, then continue. Placard has no default server: the CLI talks to the one it was logged into.
- Exit code 2 (`usage`, "no Placard server configured") → the CLI has never been logged in. Ask the user for their server address and run `placard login --base <url>`; every later command remembers it.
- Exit code 3 (`no_credentials`) → installed but not authenticated. Run `placard login`: it opens a browser page that already carries the authorization code, so the user only clicks confirm, and waits ~180 s. On headless/SSH it prints the URL instead — pass that URL and the 8-character code on to the user, any browser on any device works. Ask them to check the code matches before confirming; it cannot be done for them.

## Publish

Write the page as one self-contained `.html` file (design guidance below), then:

```bash
placard publish page.html --json
```

The response's `url` is the share link — presenting it to the user is the whole point of the task. `--json` is the form to use: stdout is exactly one JSON object, warnings go to stderr, and it carries `skill_version` (see below). Drop it only when the user wants to read the output themselves.

- `--title "Page title"`: short and recognizable — the user later finds the page by it under "My files". The page's own `<title>` always wins; `--title` only applies when the page has none, and the CLI warns when they disagree.
- `--expiry`: omitted → the page never expires. `--expiry 7d` sets a finite lifetime: `<n>d` / `<n>w` / `<n>y`, or `never`. Prefer a finite expiry for throwaway content so it cleans itself up; don't ask unless it genuinely matters.
- `--visibility`: omitted → the user's own default preference decides. `private` (only you) or `link` (anyone with the link, no account needed — say so when the page is going to someone outside the instance).
- `--password auto`: gate the page behind a 6-digit share code the server generates. The response carries it as `share_code` — this is the only time it is returned, so pass it on to the user immediately. Visitors enter it once per page. Use it when the link is going somewhere the user cannot control who reads it.
- `--open` opens the page in the user's browser right after publishing.
- Limits: 10 MB per file, HTML only, 50 publishes per user per hour. Batch revisions instead of rapid-fire republishing.

## Update and manage

Add `--id <id>` (from the publish response or `placard ls`) to update a page in place: the link never changes; a new version is created behind it. Without `--id`, publishing always creates a new page with a new link.

```bash
placard publish page.html --id <id> --json
```

- The response's `version` is what you just published; `url` is unchanged — tell the user the link stays the same.
- Re-publishing byte-identical content is idempotent: the current version is returned, nothing new stored.
- `--expiry`, `--visibility` and `--password` are all ignored on updates — a republished page keeps its original lifetime, visibility and share code (the CLI warns when you pass them anyway); ids you don't own return 404.

When revising a page published earlier in the session, update by `--id` — never publish-new-then-delete-old. Delete is destructive: the link and ALL versions die immediately, no recovery. `rm` asks a human to confirm, so it needs `-y` when you run it; ask the user before passing it, never on your own initiative.

```bash
placard ls                    # your pages: id, title, versions, visibility, views
placard open <id>             # open the share link in the user's browser
placard rm <id> -y            # delete: link and every version die immediately
```

Old versions are kept — list, pin, or restore them:

```bash
placard version ls <id>            # history, newest first, marking latest / shared
placard version pin <id> <n>       # pin the link to version <n>
placard version pin <id> latest    # back to following the newest version
placard version restore <id> <n>   # re-publish version <n>'s content as a new version
```

`restore` shares the hourly publish budget and is idempotent when the content already matches the latest.

Visibility is changed after publishing from the web app's "My files" → "Share" panel.

Exit codes: 0 ok, 2 usage error, 3 auth (not logged in / wrong base), 1 everything else. In `--json` mode a failure prints `{"error":{"code":...,"message":...}}` to stdout.

Publish and restore responses include `skill_version` — the server's current version of this document. If it is greater than the `version` in this file's frontmatter, refresh with `curl -sf -o <path-to-this-file> "<base>/skill.md"` and re-read.

Everything the CLI does is a thin wrapper over the HTTP API; if a machine genuinely cannot run the binary, `<base>/docs` documents the raw `POST /api/publish` form with a `Authorization: Bearer <token>` header.

## Page constraints

- **One page, no backend.** Nothing is deployed alongside the file: relative links don't resolve, forms have nowhere to submit, there is no storage. Multi-section content uses in-page anchors, not separate files.
- **Opaque-origin sandbox.** The page renders in `<iframe sandbox="allow-scripts">` without `allow-same-origin`: `localStorage`, `sessionStorage`, and `document.cookie` **throw** on access. Keep state in JS variables; guard any storage access with try/catch. External HTTP(S) links open in a normal new tab through a parent-page confirmation dialog with the full URL preserved; `window.open` triggers the same dialog but always returns `null`. Links to other Placard share pages (`/s/<id>`) open the same way in a new tab; other same-site relative links stay blocked. Non-HTTP protocols are blocked.
- **Prefer self-contained.** External requests are not CSP-blocked, but every CDN script or font is a load-time dependency intranet viewers may not reach. Inline CSS and JavaScript, embed images as data URIs, and the page renders anywhere the login wall lets someone in.
- **Viewers are logged-in colleagues.** The link is internal-only — fine for work data by default, but the page is visible to anyone inside the login wall who has the URL.

---

Approach the page as the design lead at a small studio known for their versatility, giving every client a visual identity pitched at the treatment the task actually calls for. Make deliberate choices about palette, typography, and layout that are specific to this subject, and avoid templated designs.

## Read the request first

Calibrate treatment, not whether to design. A doc deserves the same craft as a landing page — what changes is the treatment that craft is delivered in.

Many requests call for a more utilitarian treatment: a plan, a memo, a demo. Make it polished: include real typographic hierarchy, considered spacing, and a proper palette, but avoid over-designing. Most pages do not need a flashy, gigantic hero. Keep flourishes tasteful and limited.

Some requests call for an editorial treatment: a landing page, a game, an app or tool they'll keep or share.

When unsure: a well-composed page is never the wrong answer; an over-designed visual identity sometimes is.

Fundamentals below apply to everything. The editorial process after that runs only when the read above says so.

## Fundamentals for every page

**Honor what's already there.** Look for an existing design system first — CLAUDE.md, a tokens or theme file, existing component styles. When one exists, apply it; everything below fills gaps and never overrides. Precedence is always: the user's own words, then the project's existing system, then your choices.

**Ground it in the subject.** If the subject isn't already clear, pin it: one concrete subject, its audience, and the page's single job. The subject's own world — its materials, instruments, vernacular — is where distinctive choices come from. Build with real content throughout, never lorem.

**Pair typefaces.** Typography carries the page even when the page isn't about typography. Don't rely on webfont CDNs — a silent fallback ruins the page for intranet viewers who can't reach the CDN; inline the face as a @font-face data URI, or design deliberately on a system-font stack. Keep running text near 65 characters wide; set a type scale and stay on it; give headings `text-wrap: balance`, body text room to breathe, and uppercase labels a touch of letter-spacing.

**Choose neutrals, don't default to them.** A pure mid-grey reads as unconsidered; a grey with a slight hue bias toward the page's accent reads as chosen. Pure white and near-black are fine grounds when they suit the subject — the point is that the neutral was picked, not inherited.

**Design both themes.** The page renders in the viewer's OS theme via `prefers-color-scheme`. The robust pattern is token-level: define the palette as custom properties on `:root`, redefine only the tokens under `@media (prefers-color-scheme: dark)`, and style components through the tokens, never directly inside the media query. Give the second theme the same care as the first — don't naively invert; keep contrast legible and the accent working on both grounds. A design that deliberately commits to one visual world (a neon arcade screen, a letterpress invitation) may stay single-theme — make it a choice, not an omission.

**Let layout do the spacing.** Lay out sibling groups with flex or grid and `gap`, not per-element margins that silently collapse or double. Wide content — tables, code, diagrams — gets `overflow-x: auto` on its own container so the page body never scrolls sideways. Reach for `font-variant-numeric: tabular-nums` wherever digits line up in columns.

**Avoid AI-generated design.** AI-generated design currently clusters around a few looks: warm cream (#F4F1EA) with a serif display and terracotta accent; near-black with a lone acid-green or vermilion pop; broadsheet hairline rules with dense columns; a purple-to-blue gradient hero on white; Inter or Space Grotesk as the "safe" face; emoji as section markers; everything centered; `rounded-lg` everywhere; accent bar/rail on rounded cards. Where the user pins down a visual direction, follow it exactly — their words always win, including when they ask for one of these looks. Where nothing is specified, don't spend that freedom on one of these defaults.

**Build cleanly.** Be cognizant of overlapping elements, cascade collisions, silent font fallbacks; visual bugs hide in the gap between source and output. Close every non-void element, double-quote attributes, give keyboard focus a visible state, respect `prefers-reduced-motion`. For generative or decorative graphics, reach for Canvas or WebGL rather than hand-authoring long SVG path data.

**CSS rules.** When writing the CSS, watch your selector specificities. It is easy to generate classes that cancel each other out — a type-based selector like `.section` fighting an element-based one like `.cta` over padding and margins between sections. Structure the cascade so it doesn't silently undo your spacing.

**Writing the copy.** Words are design material, not decoration. Write from the user's side of the screen — name things by what people recognize, not how the system is built (a person manages *notifications*, not *webhook config*). Active voice; a control says exactly what happens ("Publish", then a toast that says "Published"). Errors explain what went wrong and how to fix it — no apologies, no vagueness. Specific beats clever.

**Structure is information.** Structural devices — numbering, eyebrows, dividers, labels — should encode something true about the content, not decorate it. Many generic designs use numbered markers (01 / 02 / 03), but that's only appropriate if the content actually is a sequence, like a real process or a typed timeline where order carries information the reader needs. Question whether choices like numbered markers actually make sense before incorporating them.

**When it's a UI, not a document.** A dashboard or tool is scanned and operated, not read top-to-bottom, so the craft shifts from typography to information design. Surface the summary before the detail; encode state in form as well as number — a pill, a chip, a severity stripe — so what needs attention reads at a glance. Semantic color (good / warning / critical) is separate from the accent hue and doesn't count as your accent. Give sparklines and charts the same care as type: an area fill, a faint grid, an emphasized endpoint. What's interactive should look interactive.

## Process

Before writing code, sketch a short design plan — a compact token system with color, type, and layout:

- **Color**: describe the palette as 4–6 named hex values.
- **Type**: typefaces for 2+ roles — a characterful display face used with restraint, a complementary body face, and a utility face for captions or data if needed.
- **Layout**: a layout concept in one or two sentences.

Then build, following the plan and deriving every color and type decision from it.

## When the request is editorial

The stance shifts: the client has already rejected proposals that felt templated, and is paying for a distinctive point of view. Make opinionated calls, and take one real aesthetic risk where it serves the work.

Review the design plan against the subject before building: if any part of it reads like the generic default you would produce for any similar page, revise that part, and note what you changed and why. Only after you've confirmed the plan's uniqueness do you write the code, following the revised plan exactly.

**Principles**

- The hero is a thesis: open with the most characteristic thing in the subject's world — headline, image, live demo, interactive moment.
- Typography carries the personality of the page. Pair the display and body faces deliberately, not the same families you would reach for on any other project, and set a clear type scale with intentional weights, widths, and spacing. Make the type treatment itself a memorable part of the design, not a neutral delivery vehicle for the content.
- Leverage motion deliberately. Think about where and if animation can serve the subject: a page-load sequence, a scroll-triggered reveal, hover micro-interactions, ambient atmosphere. An orchestrated moment usually lands harder than scattered effects; choose what the direction calls for. However, sometimes less is more, and extra animation contributes to the feeling that the design is AI-generated.
- Match complexity to the vision. Maximalist directions need elaborate execution; minimal directions need precision in spacing, type, and detail. Elegance is executing the chosen vision well.
- Spend your boldness in one place; keep everything around it quiet. If the accent fights the ground, shift it toward analogous or drop saturation rather than replacing it.
