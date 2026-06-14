# nved Backlog

Ideas captured but not yet scheduled. No commitment except where noted. nved is
a small REPL-flavored terminal editor (not a TUI) — print a range by line number,
climb into the block, edit in place; scrollback preserved. See `README.md` for
the user model and `~/Downloads/nved-design-spec.md` for the original design.

---

## Next

The next feature is **native CSV / TSV / DSV handling** (below). The design spec
is written — `~/Downloads/nved-csv-design-spec.md` — so Phase 1 (CSV-aware
display) is ready to plan and build. Nothing else is committed-next.

(Shipped in v0.5.0: persistent, buffer-level Ctrl+U undo. The undo stack now
lives on the buffer instead of the editing session, so Ctrl+U works at the `>`
prompt and survives climbing in and out; an undo of an edit that has scrolled
off-screen reprints it. The four inverse closures capture absolute buffer
indices instead of session-relative `cy`/`cx`.)

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

Wide tables: Phase 1 truncates with `›`; **horizontal pan is a planned follow-on
slice** (sideways twin of vertical Page-Up/Down — same block-reprint + one
`hscroll` column offset in the cursor math; line-number gutter stays FROZEN;
cursor pans automatically past the edge). No free terminal h-scrollbar (normal
screen has none; only alternate-screen apps, which nved refuses). `wrap on|off`
(default on, bare reports) is really a TEXT-mode setting that rides the SAME
hscroll machinery — build pan once, get both DSV wide-tables and text `wrap off`.
Aligned rows never wrap (row-wrap is incoherent; would break the 1-row invariant).

Structural editing (add/remove COLUMN) = PARKED, out of v1. Do it in raw mode for
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

- **Round-out decision** (leaning C): the "edit only the printed block, reprint
  to edit elsewhere" boundary vs. whole-file editing. A roam-by-paging, B inline
  managed viewport, C keep the boundary. David leans C ("dial in narrow parts by
  line number, NOT scroll huge files") — decide after dogfooding.
- **Viewport slice** — bound editing to a screenful with a scrolling viewport
  (`e.top` offset) so a mid-edit split that grows the block past the screen
  doesn't desync the cursor / scroll the header off. Subsumes the hard part of
  SIGWINCH.
- **SIGWINCH** — mid-edit resize (size is currently refreshed only at
  print/climb).
- **Search addressing (`/text/`)** — jump to the line matching a pattern and
  climb in; "find by content, not by counting." Go `regexp`. Mirrors the same
  gap in ved (which has the BRE engine but deferred the address hook).
- **v1.0 hardening** — SECURITY.md, Dependabot, CodeQL (deferred from the initial
  releases).
