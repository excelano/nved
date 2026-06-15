# nved Backlog

Ideas captured but not yet scheduled. No commitment except where noted. nved is
a small REPL-flavored terminal editor (not a TUI) — print a range by line number,
climb into the block, edit in place; scrollback preserved. See `README.md` for
the user model and `~/Downloads/nved-design-spec.md` for the original design.

---

## Next

**CSV / TSV / DSV Phase 1 (display) shipped in v0.6.0** and **Phase 2 (aligned
cell editing) in v0.7.0.** The field-layer commands (`dsv`/`quotes`/`headers`),
the record-layer `rows newline|record` reload, the `csv`/`tsv`/`asv` presets,
aligned column display with a pinned faint header, right-edge truncation, the
unbalanced-quote raw fallback, and now climb-in cell editing with
reflow-on-keystroke all ship. Reachable at startup via `+spec` (`nved +csv f`).

The Phase 2 build departed from the spec in one load-bearing way (David's call):
the display is **always raw** — both print and climb-in render the literal cell
text, so a quoted field shows its quotes everywhere and `quotes on|off` only
controls field *splitting*, never display stripping. That eliminated the
decoded-value path (`splitFields` deleted), made the climb-in view identical to
the print, and made save a **verbatim line-join** — a quoted field round-trips
with its quotes intact, no `encoding/csv` re-serialization needed (the spec's
"CSV-safe save" turned out to be free). Editing is values-only: the delimiter
key is swallowed, `Enter`-split and row joins are suppressed; `dsv off` for
structural edits. The only new cursor machinery is the horizontal
`alignedVisualCol` (the `visualCol` analog parameterized by the block's
column-width vector) — aligning turns wrap off, so the vertical math collapses
to 1 buffer line = 1 row, exactly as the spec predicted.

One boundary still held from Phase 1: the raw fallback for a multi-line quoted
field sizes its page as if rows don't wrap (`physHeight` returns 1 whenever a
delimiter is set), so a fallback block of long lines can overflow the screen by a
row. Rare (no embedded newlines in `rows record` files); a v1 punt.

**Slice 3 — wrap / horizontal pan — SHIPPED (local, post-v0.7.0).** 3a: aligned
DSV blocks wider than the terminal window to the visible columns with faint ‹ / ›
markers and pan sideways under the cursor, gutter frozen (`hscroll`, `window()`).
3b: `wrap on|off` (default on, bare reports state) rides the SAME machinery for
plain text — wrap off gives one row per line with the identical pan. The two views
now share `oneRowPerLine`/`editor.windowed`, `emitWindowedRow`, and `curVisualCol`.
Pending: dogfood, then tag v0.7.1.

**v1 roadmap — decided 2026-06-14 (post-dogfood).** Round-out is **C, locked**:
edit the printed block, reprint to edit elsewhere; no roaming viewport. David:
"C is still perfect." **Build order REORDERED 2026-06-14: find → replace →
columns.** Find/replace are fully designed and have the internal dependency chain
(replace reuses find's highlight + armed-state + chord infra); columns is
independent but still has an open addressing crux, so it follows once that design
pass is done. Slices below are numbered by the original plan; build sequence is
2 → 3 → 1.

**STATUS 2026-06-14: find (2) and replace (3) SHIPPED as one bundle — v0.8.0.
Columns (1) SHIPPED — v0.9.0** (command-only, see below). **NEXT: structural ROWS
→ v0.10.0** (unparked from this design pass — see "Rows" below). v1.0.0 stays the
hardening milestone.

1. **Slice 4 — structural columns** (`columns insert` / `columns kill`) —
   **SHIPPED v0.9.0 2026-06-14.** Addressing crux RESOLVED: **command-only, no
   chords** (every mnemonic key was taken — Insert is eaten as Copy by VTE, Ctrl+I
   *is* Tab and would shadow field-nav, Delete/Insert aren't on every keyboard).
   Columns have no gutter address, so they are named by **1-based position**,
   surfaced on demand by a faint **top index ruler** (above the header, traces
   straight down; `columns` / `c` prints it). `columns insert [N]` / `ci [N]` —
   empty column right of N (bare appends, 0 prepends); `columns kill N` / `ck N` —
   delete with confirm (names the column when headers on); bare `kill`/`ck` errors
   (destructive, must name a target). Each op is all-or-nothing across the buffer
   (a line that won't parse aborts it untouched) with ONE undo entry, and rejoins
   raw cells verbatim so quoted fields round-trip. Files: columns.go +
   printRange/printColumns (command.go) + emitRuler/columnRuler (dsv.go).

2. **Search — `find <regex>`** (short **`f`**) — BUILT 2026-06-14 as a word
   command (rest of line = the pattern, like `head 10`). Go `regexp` (RE2). Chosen
   OVER a `/regex/` slash-address: word+letter matches nved's verb shape (save/s,
   exit/x, find/f). **Find-next spelling RESOLVED: it is a literal `find next` /
   `f next` subcommand** (also short **`fn`**) — the interaction model (below)
   leaves that exact string sitting in the command line, so "next" is just a typed
   word, no collision with bare-reports-state (bare `find`/`f` still REPORTS state).

3. **Substitution — `replace /old/new/`** (short **`r`**) — BUILT 2026-06-14.
   Verb is NOT `s` (taken by save; ed only gets away with `s` because its save is
   `w`). **ed-style any-delimiter:** the first char after the verb is the delimiter
   (`r ,old,new,`). **Space after the verb in the documented form, no-space also
   accepted** (`r /…/` and `r/…/` both parse — skip optional ws after the verb,
   next char = delim). **Guard: delimiter must be non-alphanumeric** — the sane
   reading of "any delimiter" that kills the no-space word-form footgun (`replaced`
   → `d`-delimited) and the `rows`/`r` ambiguity. RE2. Capture-group backrefs in
   the replacement (`$1`, Go regexp expansion). `f`/`r` collide with ed's
   `f`=filename / `r`=read (nved uses neither; spent-symbol note, accepted).

   **REVISED 2026-06-14 (David, mid-build): global is the `all` keyword, NOT a
   `/g` suffix** — `replace all /old/new/` reads as nved's verbose idiom (also
   `r all /…/` and the short `ra /…/`). One undo entry for the whole `all` run;
   each preview-first step is its own undo entry. **Short forms added: `fn` = find
   next, `rn` = replace next, `ra` = replace all.** **Scope RESOLVED: buffer-wide
   throughout** (replace = find's twin) — the spec's "non-/g acts within the
   printed block" line was internally inconsistent with "next is buffer-spanning";
   range-scoped replace is a clean ADDITIVE mode for later if dogfooding wants it,
   not the default. Examples use `/` as the delimiter (David's call).

   **Shared interaction model — LOCKED 2026-06-14 (find + replace are twins):**
   - **Chords seed the command line.** `Ctrl+f` inserts `find ` at the prompt;
     `Ctrl+r` inserts `replace `. User types the pattern and hits Enter. **v1 is
     prompt-only**; chords firing while climbed in is the eventual target (drop to
     the prompt first) but deferred.
   - **Enter highlights, cursor stays put.** On Enter, the match is highlighted in
     the already-printed block (reverse-video reprint) but the cursor STAYS in the
     command line, which is rewritten to the armed `find next` / `replace next`.
     Enter again steps. Both are real typeable subcommands, not magic.
   - **`next` is buffer-wide.** Walks the whole buffer, reprinting the block around
     each match when it runs off the visible screenful; wraps at the end with a
     one-line "wrapped to top" notice. (This folds in the old search-addressing
     item — find IS navigation now.)
   - **Climb lands on the highlight.** A climb key (Up / Left) from the armed state
     jumps the cursor straight onto the highlighted match — the find→edit handoff,
     so dialing in by content and editing it is one motion, not two.
   - **replace is preview-first (confirm-each).** Enter on `replace /old/new/`
     highlights the first OLD match UNCHANGED (nothing written yet). `replace next`
     replaces the highlighted match and advances the highlight to the next OLD
     match. `replaced N` when exhausted. **`replace all /old/new/` is the escape
     hatch: replace ALL at once, no stepping.** Safer default — you see before you
     change.
   - **Backspace is NOT overloaded** (David's call): it deletes one char at a time,
     same as always — the user clears the armed line by holding Backspace, or with
     one **Esc**. (Consequence: typing onto an armed `… next` seed APPENDS to it —
     `fn`/`rn` are empty-prompt entry points; when armed you just press Enter.)
   - **Zero matches** → report "no match" and do NOT arm `next`.
   - **Scope: buffer-wide** for both stepping and `all` (see item 3 — the earlier
     block-scope line was dropped as inconsistent; additive range-scope possible
     later).

**Rows — structural row editing → v0.10.0 (NEXT, unparked 2026-06-14).** Columns
exposed a real gap: structural row edits are awkward. Two holes — (a) aligned DSV
view suppresses Enter-split / row-join (a mid-field split corrupts columns), so a
record can't be added/removed without `dsv off`; (b) no address-range line delete
anywhere. A `rows insert`/`rows kill` command fills both, and is the *right*
primitive for aligned mode (adds a well-formed empty record, not an unsafe
mid-field split). NOT mere symmetry with columns — rows are the primary axis and
already carry gutter addresses, so **no ruler needed**. Plan:
  - `rows insert [N]` / `ri [N]` — blank record after line N (bare appends, 0
    prepends). Empty-record SHAPE: match the column count (`,,` for a 3-col file)
    so it aligns, not a bare one-cell line. (David to confirm.)
  - `rows kill N` (or `N.M`) / `rk` — delete a line or range. Confirm only for
    RANGES / multiple, NOT a single line (less destructive than a column kill,
    which hits every row).
  - **Rename the record-separator command `rows newline|record` →
    `linebreaks newline|record`** (David's call; `recsep` rejected as wonky, plain
    complete words preferred). Frees `rows` for structural editing. Breaking change,
    no alias — nved is 0.x, ~one user, and `rows`-means-separator was always odd.
  - Open wrinkle: `ri`/`rk` sit beside replace's `r`/`rn`/`ra` (all under "r");
    mechanically free (no collision), watch for confusion.

Deferred, NOT now: **viewport + SIGWINCH** — the block-outgrows-screen desync is
still THEORETICAL (never hit in dogfooding); stays backlog as correctness
insurance, do it when it bites or with hardening. **v1.0 hardening** (SECURITY.md,
Dependabot, CodeQL) — later. Both detailed under "Later".

(Shipped in v0.5.0: persistent, buffer-level Ctrl+U undo — the stack lives on the
buffer, so Ctrl+U works at the `>` prompt, survives climbing in and out, and
reprints an off-screen edit. Cell edits and reflow compose with it for free,
since widths derive from text.)

---

## Queued — needs a design pass before code

### Native CSV / TSV / DSV handling

Make nved parse and edit delimiter-separated files ergonomically instead of
treating them as plain text. Specify the delimiter (autodetect by extension —
`.csv`→`,`, `.tsv`→tab — with `-d ';'` to override; DSV generic).

**Contrast / inspiration:** maaslalani/sheets (a Go terminal spreadsheet, 2.3k★)
is the thing to learn from and NOT clone. sheets is a full modal TUI spreadsheet
— cell coordinates (`B9`), formulas (`=SUM(B1:B8)`), visual-mode range select,
yank/paste, vim keys, `:w`/`:e`/`:goto`. nved wants CSV awareness in the
ed/ved/nved idiom: a scrolling REPL with line-number addressing and climb-in
editing, not a vim grid.

**Borrow (translated to the REPL model):**
- Column-aligned display when a block prints (fields padded to column width, like
  `column -t`; header row faint).
- Sticky header — keep the column names on screen when printing rows deep in the
  file. The faint-header-row infrastructure already exists; this is the freebie
  that makes big CSVs navigable.
- Field-aware navigation while climbed in: Tab / Shift-Tab hop cell-to-cell (the
  CSV-mode analog of the existing Ctrl+←/→ word-skip).
- CSV-safe save via `encoding/csv` — quoting and embedded delimiters survive a
  round-trip.

**Reject (spreadsheet/vim-isms that fight nved's identity):** full-screen grid
(nved has no alternate screen), modal editing, visual mode, and — the hard
boundary — **formulas**. `=SUM()` means a spreadsheet engine (dependency graph,
recalc, function library) and nved stops being a text editor. Scope is
CSV-*aware editing of values*, not computation.

**Architectural crux (why this is big — arguably bigger than the editor core):**
column alignment breaks nved's core invariant that a physical screen position
maps deterministically to a buffer rune position. Tabs were the baby version
(`expandTabs`/`visualCol`: display ≠ buffer, solved). CSV alignment is the hard
generalization: (1) a column's width depends on ALL rows in the printed block
(2D-coupled, not per-line like tabs); (2) editing a cell changes its width, which
can reflow every row's columns on a single keystroke — full-block repaint and a
moving cursor target; (3) a quoted field with an embedded newline breaks "one
buffer line == one row" entirely (likely punt initially — parse per physical
line, flag that quoted-newline CSVs aren't aligned).

**Phasing:**
1. **CSV-aware display** — aligned print + sticky header; editing either disabled
   or falls back to editing the raw `a,b,c` line (no alignment while editing).
   High value, low risk: a rendering layer over `printLines` that sidesteps the
   cursor problem because you don't edit the aligned view.
2. **Field navigation + aligned cell editing + CSV-safe save** — where the
   cursor-math, reflow-on-edit, and re-serialization complexity lives. The big
   one; depends on buffer-level undo (above) being done first so cell-edit and
   reflow mutations are undoable through the existing mechanism.

Sequenced AFTER Ctrl+U undo, both because phase 2 depends on it and because CSV
warranted its own design spec before any code. **Design spec now written:**
`~/Downloads/nved-csv-design-spec.md`. Key resolution from the spec — aligning
turns vertical wrap OFF (1 buffer line = 1 screen row), which collapses the
vertical cursor math and leaves only a horizontal `visualCol` analog (parameterized
by the block's column-width vector). Undo composes for free: widths are derived
from text, so v0.5.0's text undo restores them.

Command surface (locked in discussion): **opt-in by command, no autodetect** — a
CSV opens as plain text until you ask. **Two layers:** record layer (`rows`,
buffer-level — how bytes become lines) vs. field layer (`dsv`/`quotes`/`headers`,
view-level, never mutates the buffer — how a line becomes columns). The split is
load-bearing: `dsv` is a pure view, `rows` is a buffer reload, so order between
them barely matters and a wrong setting is obvious + one command to fix.
Commands: `dsv <delim>` (grammar = one literal char, or a name `tab`/`unit`; no
escapes, no `\xNN`, no `gs`/`fs`), `quotes on|off`, `headers on|off`, `rows
newline|record`. Presets: `csv` (= `dsv ,` + quotes on + headers on), `tsv` (=
`dsv tab` + ...), and **`asv`** (the cross-layer ASCII-separated-values preset:
`dsv unit` + `rows record` + quotes off + headers on). ASCII separators named for
what they are — `unit` (US 0x1F, field), `record` (RS 0x1E, row), not `us`/`rs`;
`dsv unit` + `rows record` literally spells `asv`. Bare `dsv`/`quotes`/`headers`/
`rows` REPORT state (standing convention — see [[feedback_bare_command_reports_state]]).
`rows` reload is lossless/reversible (re-derive bytes under new separator; resets
block + clears undo, like reopening). Startup via existing `+spec` (`nved +csv f`,
`nved +asv f`, `nved '+dsv unit' f`); no `-d` flag.

Cell nav (Phase 2): **delimiter behaves like a line ending** — arrows step *over*
it (never rest on it), so cells are mini-lines and arrows "just work." Ctrl+arrow
AND Tab/Shift-Tab both jump by cell (same "jump by meaningful unit" reflex as
word-skip; Tab also HAD to move off literal-insert — in a TSV, tab is the
delimiter, so insert-on-Tab was a footgun). Up/Down + Home/End unchanged.

Wide tables: **SHIPPED (slice 3, above).** Horizontal pan is the sideways twin of
vertical Page-Up/Down — block-reprint + one `hscroll` column offset in the cursor
math; line-number gutter stays FROZEN; cursor pans automatically past the edge,
with a one-column margin so it never lands under a ‹ / › marker. No free terminal
h-scrollbar (normal screen has none; only alternate-screen apps, which nved
refuses). The prediction held: `wrap on|off` was really a TEXT-mode setting riding
the SAME hscroll machinery — built pan once, got both DSV wide-tables and text
`wrap off`. Aligned (and wrap-off) rows never wrap, by the 1-row-per-line invariant.

Structural editing (add/remove COLUMN) = UNPARKED 2026-06-14, queued for v1 as
slice 4 (see the v1 roadmap up top; open crux = columns have no on-screen
address). Raw-mode workaround until then. Do it in raw mode for
now (`dsv off`, edit delimiters as text, re-set). If a real need appears: gated
`col add`/`col del` commands and/or **Ctrl+Insert/Ctrl+Delete** (symmetrical,
deliberate; preferred over Shift+Insert/Delete which = terminal paste/cut, and
over plain Insert/Delete since Delete = delete-char). Column delete is cross-row
data loss → always confirm ([[feedback_confirm_consequential_actions]]). REJECTED
Tab/Shift-Tab for add/del: destroys the nav reflex + Shift-Tab silently nuking a
column on a back-nav keystroke is a worse footgun than the one being fixed.

Other recs: two-space separator, save-time re-serialization via `encoding/csv`,
widths block-scoped. Slices: 1 display, 2 cell-editing, 3 wrap/pan, 4 structural
(3 and 4 independent follow-ons, order by what dogfooding makes loud).

---

## Later

- **Round-out decision — DECIDED C (2026-06-14):** edit only the printed block,
  reprint to edit elsewhere; NOT roam-by-paging (A) or a managed viewport (B).
  Confirmed post-dogfood — David: "C is still perfect." No roaming viewport.
- **Viewport slice** — bound editing to a screenful with a scrolling viewport
  (`e.top` offset) so a mid-edit split that grows the block past the screen
  doesn't desync the cursor / scroll the header off. Subsumes the hard part of
  SIGWINCH.
- **SIGWINCH** — mid-edit resize (size is currently refreshed only at
  print/climb).
- **Search addressing (`/text/`)** — FOLDED INTO v1 `find` (2026-06-14): the
  buffer-wide `find next` walk + climb-on-highlight handoff IS "find by content,
  not by counting." Kept here only as the lineage note; ved still has the same
  open gap (BRE engine present, address hook deferred).
- **v1.0 hardening** — SECURITY.md, Dependabot, CodeQL (deferred from the initial
  releases).
