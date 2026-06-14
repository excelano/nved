# nved Backlog

Ideas captured but not yet scheduled. No commitment except where noted. nved is
a small REPL-flavored terminal editor (not a TUI) — print a range by line number,
climb into the block, edit in place; scrollback preserved. See `README.md` for
the user model and `~/Downloads/nved-design-spec.md` for the original design.

---

## Next

### Persistent (buffer-level) Ctrl+U undo

Today undo is per-session: the stack lives on the `editor` struct and is
discarded when you climb out of a block, so Ctrl+U does nothing at the `>`
prompt. Make undo work after climbing out / at the prompt. The shape: relocate
the undo stack off the session onto the buffer (or repl); rewrite the 4 inverse
closures (`insert`/`splitLine`/`backspace`/`del`) to capture ABSOLUTE buffer
positions rather than session-relative `cy`/`cx` (which are offsets from
`e.start`); add a no-cursor apply path that reprints the affected line, since
there's no cursor at the prompt. This is the agreed next change.

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
warrants its own design spec before any code.

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
