---
scenarios: ["控制已打开的 Excel 工作簿"]
not_for: ["当前未接入 Excel 会话，不能操作活动工作簿"]
description: 说明活动 Excel 工作簿的操作方案；当前未接入 Excel 会话，不能操作已打开的工作簿
required_tools: [excel_session]
---

# Excel Live Control

Inspect, edit, analyze, format, and verify the workbook in the selected live Microsoft Excel session. Use connected-document tools for workbook reads and writes; use Computer Use only for application setup, target confirmation, and focus management.

On initial entry, complete the setup gates in order before session discovery. On follow-ups, reuse only the verified target and rediscover after workbook or add-in lifecycle changes.

Resolve every referenced file relative to this skill folder. Do not load the sibling artifact skill or its artifact-tool instructions while this live route is active.

## Important Instructions
- For new workbooks or authorized redesigns, plan the simplest correct workbook that meets the task, audience, actual data and domain. If formulas become hard to read, first reconsider whether the workbook’s structure, layout, or logic is overcomplicated before simplifying individual formulas. Remove unnecessary or duplicated logic while preserving calculation correctness, required business relationships, and financial reconciliation
- Instruction precedence for workbook content, layout, and formatting is: user request > reference/template > domain defaults/conventions > general defaults.

## Hard Routing Rules

Use this skill only when explicitly tagged or when the request clearly targets the Microsoft Excel desktop application, an open, active, or connected workbook in Excel Desktop, a selected range in Excel Desktop, the ChatGPT add-in for Excel, or a follow-up edit on the live-control path. For generic requests such as "create a spreadsheet," "create a workbook," or "create an Excel file" without explicit targeting of the Microsoft Excel desktop application or ChatGPT add-in, use the local workbook-authoring `Spreadsheets` skill instead. Stay on the live-control path unless the user explicitly switches targets; keep follow-up edits on the same path.

Routing selects the execution and delivery surface only; it does not override requested workbook content or a supplied reference/template.

Setup is part of the task. Use Computer Use only for setup checks and focus management, then use connected-document tools for workbook reads and writes. If a setup gate or required live capability is unavailable, stop and use the applicable exact guidance below. Do not silently switch to artifact authoring, open an unrelated workbook, or edit workbook cells through Computer Use.

Keep user-facing language to "Microsoft Excel", "ChatGPT add-in for Excel", "open workbook", "connected Excel session", or "live Excel control"; avoid internal connector/backend names.

## Setup State Machine

Complete these gates in order. A later gate cannot prove that an earlier gate passed.

Checklist: Excel app open -> intended workbook active and unambiguous -> ChatGPT add-in installed -> pane open -> signed in -> connected-document tools available -> target workbook registered.

### 1. Open Microsoft Excel And Establish The Target Workbook

Use Computer Use to inspect the Microsoft Excel application.

- If Microsoft Excel is installed but closed, open it.
- If Microsoft Excel is unavailable, use the exact **Excel unavailable** guidance below.
- If Excel shows its start screen or has no workbook open, open the workbook named by the user. If the user did not name an existing workbook, create a blank workbook.
- Wait until the workbook title, worksheet grid, and ribbon are visible. The Excel start screen is not a workbook.
- If several workbook windows are open, identify the intended workbook by title. Do not assume that the frontmost workbook is the target.
- If the request depends on the current selection, verify the selected sheet and range. If the selection is missing or ambiguous, ask the user to select it or provide an exact sheet and range.

### 2. Determine Whether The ChatGPT Add-in Is Installed

Inspect the Home ribbon only after a workbook grid is active.

- A visible `ChatGPT` button on the ribbon is positive evidence that the add-in is installed.
- If the button is absent, open **Home > Add-ins** and look for **ChatGPT** published by **OpenAI, LLC**. Do not infer that the add-in is missing only because its task pane is closed.
- If ChatGPT is not present in the ribbon or the installed add-ins list, treat the add-in as not installed.

For a missing add-in, give the user these exact choices:

1. Open the official Microsoft Marketplace listing: https://marketplace.microsoft.com/en-us/product/office/WA200010215
2. Or, in Excel, go to **Home > Add-ins**, search for **ChatGPT**, verify that the publisher is **OpenAI, LLC**, and choose **Add** or **Get it now**.
3. Return to the target workbook and open **ChatGPT** from the ribbon.

Installing an add-in is a user-controlled software-install action. Ask the user to complete the final install step, then resume inspection. If the Microsoft Marketplace or Office add-in store is blocked by organization policy, ask the user to contact their Microsoft 365 administrator. The official OpenAI setup and admin-deployment guidance is at https://help.openai.com/en/articles/20001063-chatgpt-for-excel/.

### 3. Open The ChatGPT Add-in Pane

If the add-in is installed but its pane is not visible, click **ChatGPT** on the Home ribbon. Allow a few seconds for the task pane to load, then inspect the pane again.

- A ribbon button without a visible task pane means installed but not open.
- A task pane titled **ChatGPT** means the add-in is open, but it does not by itself prove that the user is signed in.
- If Excel shows an add-in load or restart error, retry opening the pane once. If the same error returns, stop and tell the user what Excel displayed. For a recurring Windows SSO add-in error, direct the user to OpenAI Support as described in the official setup guidance.

### 4. Verify ChatGPT Sign-in

Inspect the contents of the open task pane.

- A normal chat surface such as **New chat** with the composer text **Ask anything, @ for context** is positive evidence that the add-in is signed in.
- A **Sign in**, **Log in**, **Get started**, account-choice, or workspace-access screen means sign-in is incomplete.
- Do not infer sign-in from the ribbon button, the pane title, or a previously signed-in browser session.

If sign-in is incomplete, ask the user to take over and finish sign-in with the ChatGPT account and workspace they intend to use. The user must handle credentials, account choice, SSO, and MFA. If the workspace says the add-in is disabled, the user needs a ChatGPT workspace administrator to enable **ChatGPT for Excel and Sheets** in workspace permissions. Resume only after the normal chat composer is visible.

### 5. Verify Connected-Document Tool Availability

After Excel, the target workbook, the open add-in pane, and sign-in are all verified, check whether `list_document_sessions` is available in the current Codex thread.

- If the tool is unavailable, do not report that the workbook failed to register. The current Codex thread did not load the connected-document tool surface.
- Tell the user to confirm that the Spreadsheets plugin is installed or reinstalled, then start a new Codex thread and retry the Microsoft Excel request. A thread does not necessarily acquire newly installed plugin tools after it has started.

### 6. Verify Workbook Registration

Call `list_document_sessions(surface="excel")` only after the previous gates pass.

- If exactly one session matches the target workbook, select it.
- If several sessions match and the target is unclear, ask the user which workbook to use.
- If sessions exist only for other workbooks, do not send commands to them. Activate the intended workbook, open its ChatGPT pane, keep the pane visible, and retry discovery.
- If no Excel session exists, keep the target workbook active, select a cell in it, keep the signed-in ChatGPT pane open, wait briefly, and retry once.
- If no session appears, close and reopen the ChatGPT pane once, wait for the normal composer, and retry once.
- If the workbook still does not register, tell the user that Excel and the add-in are ready but Codex cannot see a connected session. Ask the user to reopen the target workbook or restart Excel, then reopen ChatGPT and sign in if prompted. Do not loop indefinitely.

Workbook registration is tied to the current workbook and add-in lifecycle. Rediscover sessions after the workbook is renamed or saved under a new name, after the add-in reloads, after Excel recovers or restarts, or when a previously working command reports a missing or stale session. Never reuse an `executor_session_id` merely because its workbook title looks similar.

### Exact User Guidance For Incomplete Gates

Use the smallest applicable message and wait for the user when their action is required:

- **Excel unavailable:** "I cannot find the Microsoft Excel desktop app. Install or make Microsoft Excel available, open it, and tell me to continue. I will not switch this request to another spreadsheet workflow unless you ask me to."
- **Target workbook unclear:** "Microsoft Excel has more than one workbook open, and I cannot safely identify the target. Tell me the workbook title to use, or bring that workbook to the front and tell me to continue."
- **Add-in missing:** "Microsoft Excel and the workbook are open, but ChatGPT for Excel is not installed. Install the OpenAI add-in from https://marketplace.microsoft.com/en-us/product/office/WA200010215, or use Home > Add-ins in Excel and search for ChatGPT by OpenAI, LLC. Open ChatGPT from the ribbon when installation finishes, then tell me to continue."
- **Installation blocked:** "Your organization is blocking the Microsoft Marketplace or the ChatGPT add-in. Ask your Microsoft 365 administrator to deploy ChatGPT for Excel using the admin guidance at https://help.openai.com/en/articles/20001063-chatgpt-for-excel/. After the add-in appears in Excel, open it and tell me to continue."
- **Pane closed:** "ChatGPT for Excel is installed, but its task pane is closed. Open ChatGPT from the Home ribbon and keep the pane visible, then tell me to continue."
- **Signed out:** "The ChatGPT pane is open, but sign-in is not complete. Please finish sign-in, account/workspace selection, and any MFA in the pane. When you see New chat and the Ask anything composer, tell me to continue."
- **Tools unavailable:** "Excel and ChatGPT for Excel are ready, but this Codex thread does not have the connected Excel tools. Reinstall or enable the Spreadsheets plugin if needed, then start a new Codex thread and retry this request."
- **Wrong workbook registered:** "Codex can see an Excel workbook, but it is not the workbook you asked me to use. Activate the target workbook, open its ChatGPT pane, and tell me to retry. I will not send commands to the other workbook."
- **Workbook not registered:** "Excel and the signed-in ChatGPT pane are ready, but Codex cannot see this workbook yet. Keep the target workbook active, reopen the ChatGPT pane, and tell me to retry. If it still does not connect, reopen the workbook or restart Excel and open ChatGPT again."

## Live Commands

Before live commands, fetch the selected session's tool schemas with `get_document_tool_schemas`, then call `execute_document_command` with the exact `executor_session_id`, schema-valid args, and a caller-stable `idempotency_key`. Treat advertised schemas as the live-control contract.

Default to direct live workbook tools when the selected session advertises them: `read_ranges`, `search_workbook`, `list_items`, `write_range`, `clear_range`, `update_sheet`, `update_workbook`, `copy_range_to`, `read_range_image`, `read_sheets_metadata`, `resize_range`, `update_sheet_view`, `format_range`, `chart`, `table`, and `pivot_table`. The session may also advertise `run_officejs`; use it only under the Office.js gate below.

### Live Workbook Quality Checklist

For generated workbooks, source-to-workbook conversions, analytical trackers, and substantial workbook edits, apply the shared workbook quality rules and live completion rules in this skill before final response.

Minimum live verification:

- Inspect key values and formulas after writes; resolve formula errors, blank outputs, broken references, and obvious mismatches with the requested logic.
- Follow `features/charts.md` for chart source and object verification. For tables and PivotTables, verify source ranges before creation and confirm the expected native objects with `list_items` when available.
- Use `read_range_image` for charts, dashboards, dense presentation tables, or substantial layout changes; fix blank charts, clipped headers or numbers, unreadable formatting, and obvious layout overflow.
- For long multi-sheet builds, verify and format each user-facing sheet before moving far ahead; do not defer all content checks and layout repair until the end.
- For dashboards, reports, scorecards, and trackers, apply the relevant layout and formatting guidance from `style_guidelines.md` and chart decision rules from `features/charts.md` when those files are required.
- Do not treat successful setup, a completed command, or a saved workbook as task completion until the workbook content has passed the relevant checks.

When the user expects a file from a live Excel session, save through the selected session only when an advertised command supports save or export behavior. If no such command is available, report that limitation and leave host-global save or recovery unchanged.

If the selected session does not advertise a tool needed for the request, follow the workbook-registration rediscovery rules once if the workbook or add-in changed. Otherwise report the missing live capability and wait for the user to repair setup or explicitly switch targets.

## Office.js Gate

Before calling `run_officejs` for any reason, read `officejs.md` completely in the current turn and follow its decision boundary and instructions, even when the schema is already available. The hard routing rules above continue to govern setup, Computer Use, and fallback behavior.

## Out Of Scope

Do not use live Excel control for Google Drive, Google Sheets, or other cloud spreadsheet connector requests.

Do not claim live control can install desktop apps, change OS or Excel settings, enable macros, use COM/win32com, run native print/PDF/export/page-setup workflows, bypass workbook protections, or perform commands not advertised by the selected session.

Treat spreadsheet-processing code questions as software implementation or debugging unless the user also asks to control a connected Excel session.

## Writing Quality and Authored Content
For newly authored content, including additions during edits:

- Write for intended audience. Never include internal file paths, authoring commentary, planning notes, or requester instructions in the artifact unless explicitly requested. Do not repeat audience or style directives such as “executive-friendly” in headings, content, or comments.

- Use concise, literal subject titles and labels. Put company, timeframe and source context in subtitles or nearby notes.
  - Good: `Weekly metrics`. Bad: `Follow the weekly trends`
  - Good: `Monthly results`. Bad: `Decision-ready monthly impact analysis`
  - Good: `Income and household assumptions`. Bad: `Same paycheck. Different purchasing power.`

- Prefer direct, specific human wording. Avoid slogans, buzzwords, invented terminology, vague framing and formulaic claims.
  - Good: `Contributions decreased`. Bad: `Contributions waned`
  - Good: `Permanent drop in commuting`. Bad: `Structurally lower commute base`
  - Good: `Revenue metrics`. Bad: `Strategic Value Drivers`

- Avoid AI-like sentence constructions when simpler wording is clearer:
  - Semicolons: `Travel demand and employment from Jan to Feb. Persistent behavior shifts are shaping recovery.` not `Travel demand and employment fell from Jan to Feb; persistent behavior shifts are shaping the path back.`
  - Passive voice: `The team approved the proposal.` not `The proposal was approved by the team.`
  - Contrast slogans like `It’s not X, it’s Y`: `Humidity exposure over time` not `Humidity is an exposure trajectory, not a setpoint.`

- Keep wording factual, parseable and supported by the workbook.
  - Good: `Transit use is at 79%, matching pre-pandemic levels`
  - Bad: `79% Transit use back to pre-pandemic`

- Avoid AI-style decoration in titles and labels: bullets, icons, emoji, pipe-delimited titles, decorative arrows, or generic suffixes such as `review`, `impact`, `analysis`, or `dashboard`.
  - Good: `$ in USD`
  - Bad: `$ in USD • monthly • forecast`

- For checks and logic, be specific:
  - Bad: `Signal integrity: BLOCKED`. Good: `Missing input: forecast rate` (a specific functional warning)

- Do not include motivational wording, self-assessment, repeated setup. Do not add decorative badges, confidence ratings, status tags or PASS/WARN/BLOCKED banners.
  - Bad: `This workbook is source-backed and ready for review`. Omit the self-assessment, and keep needed sources and limitations besides analysis if actually useful.

User requests and preferences always take priority. For edits, follow existing writing style in the workbook.


## Workflows
Required:
- `workflows/edit_workflows.md` for existing files/follow-ups.
- `workflows/create_workflows.md` for new files

## Bundled Guidance

- `style_guidelines.md`: required when generating or substantially formatting a workbook.
- `features/charts.md`: read when creating or editing charts, or when a visual summary would clarify KPIs, comparisons, trends, breakdowns, rankings, or progress.
- `references/image-references.md`: read when a reference image or screenshot is provided.
- `references/read_only_qna.md`: read for questions or audits that do not modify the workbook.
- `officejs.md`: read completely before using `run_officejs`; do not read it for direct-tool-only work.

Load only the relevant files in this skill folder. Do not follow references from the sibling artifact skill.

## Domain Requirements
Read only relevant guidance:
- Finance and investment banking: `domain_guidance/financial_models.md`
- Corporate finance and FP&A: `domain_guidance/corporate_finance_fpa.md`
- Healthcare: `domain_guidance/healthcare.md`
- Marketing and advertising: `domain_guidance/marketing_advertising.md`
- Scientific research: `domain_guidance/scientific_research.md`

## Create and Edits
For any task that requires modifying or creating a workbook:

### Formula Correctness
Apply to newly added or edited formulas, alongside the relevant create/edit workflow.

- Keep raw data, assumptions, editable mappings, scoring rules and thresholds in labeled inputs/tables. Mathematical, index and control constants may remain in formulas.
- Keep calculated outputs formula-driven so they update with inputs. Use consistent patterns across comparable rows and projection periods, preserving intentional differences. Reuse shared results; keep independent reconciliation checks independent.
- Use the simplest correct, human-readable formula. Formulas must be **easily auditable**. Do not perform complex calculations in a single cell when possible. Instead, use helper cells for intermediate values, direct references, arithmetic, aggregates and lookups like INDEX/MATCH/XLOOKUP, SUMIFS etc. Use supported LET, IF or arrays only when they improve clarity. Users should be able to trace the model from inputs to outputs easily.
- Reuse results or shared checks only when inputs, periods, units, rounding and overrides match; gate only affected outputs. Add helpers for meaningful repeated work, not trivial expressions; narrow edits do not authorize new helper ranges. Compute shared intermediate calculations once in labeled helper cells.
- Make formulas copy/fill-safe: reference destination headers/IDs, anchor only fixed sources, and use keyed lookups when layouts differ. Derive period filters/labels from destination keys;
- Quote cross-sheet names, e.g. ='Sheet Name'!A1.
- Keep workbook validation useful and proportional to realistic input risks. Reuse checks and separate them from calculations. Block outputs only when invalid inputs would make them misleading; do not invent business restrictions to validate inputs.
- Handle expected missing/invalid inputs explicitly; avoid blanket IFERROR wrappers or plausible-zero substitutes for unexpected errors. When simplifying, preserve calculation meaning, intended blank/error behavior, one-offs and overrides. Remove redundant guard layers while preserving checks that expose invalid source data.
- Scale verification to complexity and risk: check references/results for simple formulas; test representative inputs, copies and affected outputs for complex or consequential calculations. Keep authoring-only tests out of the workbook.
- For source-backed analyses, spot-check representative outputs and reconcile key totals with source definitions.
- Use numeric tolerances consistent with required calculation precision; compare identifiers, integer counts and categories exactly.

### Data Formatting Rules
- Store numbers, percentages, currency, and dates as typed spreadsheet values, not preformatted strings. Use text only for true identifiers such as ZIP codes, account IDs, SKUs, or labels.
- Use Excel-invariant number/date format codes, not locale-specific display strings. Generic examples include `#,##0`, `#,##0.0`, `0.0%`, `0.00%`, `"$"#,##0`, `"$"#,##0.00`, `yyyy-mm-dd`, `mmm yyyy`. Existing workbook/reference, and domain conventions take priority;
- Percentages: Follow the domain or reference's precision. Otherwise, use 1 decimal for most analytical cells, 0 decimals for dashboard outputs, and 2 decimals where small rate differences matter.
- Do not swap `.` and `,` in format codes to mimic locale separators; separators are controlled by spreadsheet/render locale. Use `0.0%`, not `0,0%`, and `#,##0`, not `#.##0`.
- Choose the appropriate format for readability. Match precision to meaning: counts use `#,##0`; rates usually use `0.0%` or `0.00%`; currency uses whole units unless cents matter.

### Verification Rules
Before final response, apply these checks within the authorized changes and their dependencies. Report unrelated pre-existing defects without repairing them.

Use only tools advertised by the selected Excel session, with their advertised schemas.

1. Inspect key ranges:
- Read key values and formulas after writes with `read_ranges` or an advertised equivalent.
- Use workbook metadata, range reads, and object inspection to capture the pre-edit baseline and check for unintended changes across tabs.
- Confirm chart, table, and PivotTable objects and their source ranges with `list_items` and other advertised reads when available.

2. Scan formula errors:
- Use `search_workbook` or an advertised equivalent to locate `#REF!`, `#DIV/0!`, `#VALUE!`, `#NAME?`, `#N/A`, `#NUM!`, `#NULL!`, `#SPILL!`, and `#CALC!` errors.

3. Verify visual output with `read_range_image` or an advertised equivalent:
- For long multi-sheet builds, verify each user-facing sheet as it is built rather than deferring all checks until the end.
For creation or broad authorized restructuring, visually review every sheet. For a narrow edit, review the changed view and affected dependencies, then compare all tabs with the source for unintended value, formula, object, validation or style changes. Do not repeatedly render unchanged tabs; investigate any scope-preservation failure.

Visual requirements:
- Fix severe defects before finalizing: blank/broken charts, low-contrast text, unreadable font sizes, clipped headers/numbers or chart data/axis labels, obvious formula errors, default blank sheets, or content outside the visible working area.
- Ensure logical labels or titles appear once and have a clear layout
- Ensure text is visible and columns/rows are appropriately sized; verify chart labels, axis ticks and fonts at normal zoom.


4. Keep verification compact:
- Use native Excel tools to verify requested features and results, reusing checks for unchanged content.
- Successful setup or a completed command does not prove that the requested workbook content is correct. Apply the shared checks to the connected workbook itself.
- Avoid arbitrary formula count checks, assumptions about file storage, and huge diagnostic dumps.

5. Finalize when the connected workbook passes the checks above.
- Follow this skill's setup and missing-capability rules if a required live check is unavailable. Do not substitute Artifact Tool or require a local workbook export to verify a live edit.

### Citation Requirements
- Cite sources inside the spreadsheet
- Use plain-text URLs in spreadsheet cells.
- For financial models, preserve provenance through existing source conventions, a compact source table or an existing supported cell annotation. Prefer a table for repeated inputs; do not force a new table into a narrow edit.
- Do not add cell comments or cell notes unless the user requests them. Preserve existing annotations; put needed new source or assumption context in ordinary cells within scope.
- For researched row-wise data tables, include source URLs in a dedicated source column.
- When comments are requested, keep them succinct, minimal and easy to read.
- Use one supported annotation path per cell; update an existing note/thread rather than layering another system over it. Reject duplicate cell references in a legacy comment part. Repair the authoring path rather than deleting provenance to make export succeed.

## Completion

- Complete a live edit only after the connected workbook contains the requested changes, key values and formulas have been inspected, and native objects have been confirmed when relevant.
- Use `read_range_image` or the advertised equivalent for charts, dashboards, dense presentation tables, or substantial layout changes. Fix material clipping, overlap, blank charts, unreadable formatting, and visible formula errors before finishing.
- Do not require a local `.xlsx` export unless the user explicitly requests one and the selected session advertises a supported save or export command.
- For question-only requests, answer from the connected workbook context without editing unless the user asks for a change.
- If setup or a required live capability is blocked, use the smallest applicable user guidance from this skill and wait. Do not silently switch to artifact authoring.

## Error Recovery
On first tool or API error:
1. Read error text.
2. Consult the selected workflow's targeted help or schema discovery only if needed.
3. Retry with minimal patch (not full rewrite).
4. Continue from existing workbook state.

Do not loop indefinitely on similar failures.

## Source, Comment, And Attachment Rules

- Keep source notes compact: record the source name, section or table label, URL when available, and enough context to audit the number.
- If the authenticated profile provides a display name, use it as the threaded-comment author unless the user requests another name. Default to `User`.
- Use bundled extraction libraries only for supporting analysis. Keep auditable and user-editable calculations in workbook formulas, then write results through the connected Excel session.
