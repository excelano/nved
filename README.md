# nved

A small terminal text editor that feels like a REPL rather than a full-screen
application. You print a range of lines by number, climb into the printed block
with the arrow keys, and edit it in place. There is no alternate screen and no
takeover of your terminal: output scrolls normally and your scrollback stays
intact, the way `cat` or an old line editor leaves it.

nved is a from-scratch descendant of [ved](https://github.com/excelano/ved), an
`ed` clone. Where ved replays commands against text, nved lets you move a real
cursor through committed lines and change them directly. It is deliberately
small and single-purpose — one window, one buffer — and best suited to dialing
in a specific part of a file by line number, not to scrolling through large
files.

## Install

On Debian or Ubuntu, add the [Excelano apt repository](https://excelano.com/apt/)
once and updates flow through `apt upgrade`:

```sh
curl -fsSL https://excelano.com/apt/setup.sh | sudo sh
sudo apt install nved
```

With a Go toolchain:

```sh
go install github.com/excelano/nved@latest
```

## Build from source

```sh
git clone https://github.com/excelano/nved
cd nved
go build -o nved .
```

Requires Go 1.25 or newer.

## How it works

Start `nved` with a file name, or with no argument for an empty unnamed buffer.
At the `>` prompt you address lines by number:

| Command    | Effect                                  |
|------------|-----------------------------------------|
| `N`        | print line N                            |
| `N.M`      | print lines N through M                 |
| `N.`       | print from line N to the end            |
| `.N`       | print from the start to line N          |
| `.`        | print the whole file                    |
| `$`        | print the last line                     |
| `$-N`      | print the Nth line before the last      |
| `head [N]` | print the first screenful, or the first N lines |
| `tail [N]` | print the last screenful, or the last N lines |
| `s [name]` | save; a name is required when unnamed (also `Ctrl+S`) |
| `x`        | exit; warns once when dirty (also `Ctrl+X`, `q`, `quit`) |
| `h`        | show the command and key reference (also `H`, `?`) |
| `wrap on\|off` | word-wrap long lines, or show one per row and pan sideways |

The `.` separator sits under the right hand on the numeric keypad, where there is
no comma; a `,` works in its place anywhere — the two are interchangeable.

A number can be offset from the end with `$-N`, so `$-9.$` is the last ten lines.
`tail` is the everyday shorthand for that: bare `tail` brings the last screenful
on screen ready to climb into, and `tail 10` shows the last ten lines. `head` is
its mirror at the top. The offset is `ed`'s address arithmetic, restricted to the
`$` anchor since nved has no roaming current line.

You can also open straight to a range. A `+spec` argument — `spec` is any of the
commands above — runs once on startup, so the block is already on screen at the
prompt:

```sh
nved +42 notes.txt        # open with line 42 printed
nved +10.30 notes.txt     # ... lines 10 through 30
nved +tail notes.txt      # ... the last screenful, ready to edit
```

Out-of-range numbers clamp to the nearest valid line. A range taller than the
terminal prints one screenful from the top; **Page-Up** and **Page-Down** reprint
the screenful above or below so you can walk through it.

The printed block sits just above the prompt. Climb into it to edit:

- **Up** lands on the bottom line, **Left** at the end of the last line, **Ctrl+Home** on the first line.
- Arrows move the cursor; **Ctrl+Left** / **Ctrl+Right** skip by words; **Home** / **End** and **Ctrl+Home** / **Ctrl+End** jump to the edges.
- **Enter** splits a line, **Backspace** and **Delete** join lines, typing inserts.
- **Ctrl+U** undoes the last edit. The history lives with the buffer, so it works at the `>` prompt too and survives climbing in and out — if the undone edit has scrolled off, nved reprints it.
- **Ctrl+S** saves in place without leaving (your cursor stays put); **Ctrl+X** exits — the save and exit chords work while editing, just as at the prompt.
- Leave the editor with **Esc** or **Ctrl+C**, or by stepping off the bottom (**Down**) or off the end of the last line (**Right**) — the mirror of how you climbed in.

Long lines are word-wrapped by nved itself, with a continuation indent that lines
the wrapped text up under the gutter. `wrap off` turns that off: each line then
takes a single row, and a line wider than the terminal shows a faint `‹` or `›`
where it runs off the side — climb in and the row pans sideways to follow the
cursor, the gutter held fixed at the left. It is the plain-text counterpart of the
aligned column view's horizontal pan. `wrap on` restores wrapping, and a bare
`wrap` reports the current setting. Edits stay inside the printed block; to edit
elsewhere, print that range and climb into it.

## Viewing delimited files (CSV, TSV, DSV)

A `.csv` is a text file, so nved opens it as plain text — nothing happens until
you ask. Set a delimiter and the printed block renders as aligned columns:

```
> csv
```

`csv` is a preset; underneath it are independent switches you can set or tune on
their own:

| Command                | Effect                                                |
|------------------------|-------------------------------------------------------|
| `dsv C`                | split each line into columns on `C` — a character, or the name `tab` or `unit` |
| `dsv off`              | back to plain text                                    |
| `quotes on\|off`       | respect `"quoted,fields"` when splitting              |
| `headers on\|off`      | draw line 1 as a faint column header, pinned when it scrolls off |
| `rows newline\|record` | the record separator (`record` is the ASCII record separator, 0x1E) |
| `csv` `tsv` `asv`      | presets — `csv` is `dsv ,` with quotes and headers on, `tsv` the same with tabs, `asv` the ASCII-separated set (`unit` fields, `record` rows); `csv off` (or `tsv`/`asv off`) is `dsv off` |

Each switch invoked bare — `dsv`, `quotes`, `headers`, `rows` — reports its
current state instead of changing it. Columns are sized to the widest cell on
screen, and the delimiter itself is drawn faint in the gap between them, so a row
reads `a , b` — you see what actually separates the fields, the same reason
quoted cells keep their quotes. A row wider than the terminal shows a faint `‹`
or `›` where content runs off the side; climb in and the columns pan sideways to
follow the cursor, the way Page-Up and Page-Down follow it down the file, with
the line-number gutter held fixed at the left.

The two ASCII information separators are worth knowing: `unit` (0x1F) for fields
and `record` (0x1E) for rows never occur in ordinary text, so a file delimited by
them needs no quoting or escaping at all — `asv` is the one-word way to read one.

Because the view is reached through ordinary commands, `+spec` opens straight into
it:

```sh
nved +csv data.csv
nved +asv data.bin
```

Climb into an aligned block and you edit it in place, cell by cell. `Tab` and
`Shift-Tab` (or `Ctrl-Left` / `Ctrl-Right`) move field to field, and the grid
re-aligns as you type — widen one cell and the columns below it shift to match.
Editing changes the field value only: the delimiter key is swallowed, and
`Enter`-split and row joins are suppressed, so you can't accidentally restructure
the file from inside a cell. The one exception is a quoted cell — inside the
quotes the delimiter is data, so typing a comma in `"a,b"` works; to put one in a
plain cell, quote it first by typing `"` at each end. For structural edits —
splitting a row, adding a column — `dsv off` back to plain text. A quoted field
keeps its quotes on screen and on save, so you always see which cells are quoted
and which aren't.

## License

MIT — see [LICENSE](LICENSE). Authored by David Anderson, with AI assistance.
