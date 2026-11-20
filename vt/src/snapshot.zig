//! The snapshot: a VT byte sequence that recreates the shadow terminal's
//! state in a blank terminal of the same size.
//!
//! The approach is adapted from zmx's `serializeTerminalState` (src/util.zig,
//! https://github.com/neurosnap/zmx), which carries this licence:
//!
//!   Copyright (c) 2025 Eric Bower
//!
//!   Permission is hereby granted, free of charge, to any person obtaining a
//!   copy of this software and associated documentation files (the
//!   "Software"), to deal in the Software without restriction, including
//!   without limitation the rights to use, copy, modify, merge, publish,
//!   distribute, sublicense, and/or sell copies of the Software, and to
//!   permit persons to whom the Software is furnished to do so, subject to
//!   the following conditions:
//!
//!   The above copyright notice and this permission notice shall be included
//!   in all copies or substantial portions of the Software.
//!
//!   THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS
//!   OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
//!   MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
//!   IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
//!   CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
//!   TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
//!   SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
//!
//! Taken from zmx: the two phases (scrollback rows first, then the visible
//! screen), leaving out synchronised output, the OSC 7 pwd without its NUL
//! sentinel, the title, and the OSC 10/11/12 colour overrides.
//!
//! Changed here:
//! - Scrollback rows are pushed into the terminal's scrollback with line
//!   feeds, not followed by ED 2, so no row is erased.
//! - Rows are written cell by cell here (RowEncoder), not with Ghostty's
//!   formatter, and soft-wrapped rows stay wrapped.
//! - Per-cell OSC 8 hyperlinks are kept.
//! - Both screens are written when the alternate screen is active.
//! - Modes come after the text, so modes such as DECAWM, IRM and DECOM
//!   cannot change how the text lands.
//! - The kitty keyboard flags are pushed as a stack, per screen.
//! - Mouse modes follow the order the program set them in.
//! - The saved cursor (DECSC), the cursor shape and the palette are kept.
//!
//! The output never contains RIS or a query.

const std = @import("std");
const vt = @import("ghostty-vt");

const Error = std.Io.Writer.Error;

pub const Options = struct {
    /// The most scrollback rows to include.
    max_scrollback: u32 = 0,
};

/// Writes the snapshot. The terminal must already have the size the
/// snapshot is for.
pub fn write(term: *vt.Terminal, w: *std.Io.Writer, opts: Options) Error!void {
    const primary = term.screens.get(.primary).?;
    const alt_active = term.screens.active_key == .alternate;

    // Grapheme clustering decides how wide some text is, so it goes first.
    if (term.modes.get(.grapheme_cluster) != term.modes.default.grapheme_cluster) {
        try writeMode(w, .grapheme_cluster, term.modes.get(.grapheme_cluster));
    }

    // Phase 1: the primary screen's scrollback. These rows scroll into the
    // terminal's own scrollback, so search there finds them.
    var pushed = false;
    if (opts.max_scrollback > 0) scrollback: {
        const pages = &primary.pages;
        const active_top = pages.getTopLeft(.active);
        const screen_top = pages.getTopLeft(.screen);
        if (active_top.eql(screen_top)) break :scrollback;
        var bottom = active_top.up(1) orelse break :scrollback;
        bottom.x = pages.cols - 1;
        const top = active_top.up(opts.max_scrollback) orelse screen_top;

        if (!try writeRows(w, top, bottom)) break :scrollback;

        // One line feed per screen row moves every row written above,
        // and anything that was on the screen before, into scrollback.
        try w.splatBytesAll("\r\n", term.rows);
        pushed = true;
    }
    if (!pushed and term.rows > 1) {
        // A blank terminal stays as it is. A terminal with text above the
        // cursor moves that text into its scrollback instead of losing it.
        try w.splatBytesAll("\r\n", term.rows - 1);
    }

    // Phase 2: the primary screen's visible rows.
    try w.writeAll("\x1b[H");
    try writeActive(primary, w);

    if (alt_active) {
        const enter = altMode(term);

        // What the primary screen gets back when the program leaves the
        // alternate screen: its saved cursor, and its keyboard flags.
        if (enter == .alt_screen_save_cursor_clear_enter) {
            // 1049 saves the cursor as it enters, and restores it on exit.
            if (primary.saved_cursor) |s| {
                try writeCursorAt(w, s.x, s.y);
                const st = s.style;
                try w.print("{f}", .{st.formatterVt()});
            } else {
                try writeCursorAt(w, primary.cursor.x, primary.cursor.y);
            }
        } else {
            try writeSavedCursor(primary, w);
            try writeCursorAt(w, primary.cursor.x, primary.cursor.y);
            const st = primary.cursor.style;
            try w.print("{f}", .{st.formatterVt()});
        }
        try writeKitty(primary, w);

        try writeMode(w, enter, true);
        try w.writeAll("\x1b[0m\x1b[H\x1b[2J");
        try writeActive(term.screens.active, w);
    }

    try writeState(term, w);
}

/// The mode that switched to the alternate screen. 1049 wins when more
/// than one is set, as it saves the primary cursor.
fn altMode(term: *const vt.Terminal) vt.Mode {
    if (term.modes.get(.alt_screen_save_cursor_clear_enter)) return .alt_screen_save_cursor_clear_enter;
    if (term.modes.get(.alt_screen)) return .alt_screen;
    if (term.modes.get(.alt_screen_legacy)) return .alt_screen_legacy;
    return .alt_screen_save_cursor_clear_enter;
}

/// Writes rows from top to bottom, both inclusive, from column 0 of an
/// empty row. Returns whether it wrote anything. See RowEncoder.
fn writeRows(w: *std.Io.Writer, top: vt.Pin, bottom: vt.Pin) Error!bool {
    var enc: RowEncoder = .{ .w = w };
    var it = top.rowIterator(.right_down, bottom);
    while (it.next()) |pin| try enc.row(pin);
    try enc.finish();
    return enc.wrote;
}

/// Writes rows cell by cell: text, SGR styles and OSC 8 hyperlinks.
///
/// Ghostty's own VT formatter is not used for the text. At commit
/// ad97d331, the pin before libghostty-vt 1.0.0, it wrote skipped cells
/// (after a tab, say) as spaces in the style of the cell before them, so a
/// background colour or an underline spread. It also took a row that held
/// only background colour for an empty row, which moved every row after it.
///
/// - Blank cells inside a row are skipped with CUF, so they stay empty.
/// - A soft-wrapped row is written to its last column with no line break,
///   so the terminal wraps it again and keeps the wrap.
/// - Trailing blank cells and trailing blank rows are left out.
const RowEncoder = struct {
    w: *std.Io.Writer,
    /// The pen as last written.
    style: vt.Style = .{},
    /// The hyperlink as last written, if one is open.
    link: ?Link = null,
    /// Rows to move down before the next text.
    advance: usize = 0,
    /// How the last row written ended, if it wraps.
    wait: Wait = .none,
    wrote: bool = false,

    const Link = struct { id: ?[]const u8, uri: []const u8 };

    const Wait = enum {
        none,
        /// A character fills the last column. The cursor waits there, and
        /// the next printed character starts the next row.
        pending,
        /// The last column holds a spacer for a wide character that did
        /// not fit. The cursor is on it; the wide character, printed
        /// next, makes the spacer and starts the next row.
        head,
    };

    fn row(e: *RowEncoder, pin: vt.Pin) Error!void {
        const r = pin.rowAndCell().row;
        const cells = pin.cells(.all);

        var end: usize = 0;
        for (cells, 0..) |*cell, x| {
            if (!blankCell(pin, cell)) end = x + 1;
        }
        if (r.wrap) end = cells.len;
        if (end == 0) {
            e.advance += 1;
            return;
        }

        // Move down to this row. From the end of a wrapped row, the first
        // line break only reaches the row the wrap would have started.
        if (e.advance > 0) {
            const breaks = e.advance + @intFromBool(e.wait != .none);
            try e.plain();
            try e.w.splatBytesAll("\r\n", breaks);
            e.wait = .none;
        }
        e.advance = 0;
        e.wrote = true;
        var first = true;

        var blanks: usize = 0;
        for (cells[0..end], 0..) |*cell, x| {
            if (cell.wide == .spacer_tail) continue;
            if (cell.wide == .spacer_head and x == cells.len - 1 and r.wrap) {
                // Stop on the spacer. The wide character that starts the
                // next row puts it back.
                if (first) try e.wrapInto(false);
                first = false;
                try e.skip(blanks);
                e.wait = .head;
                return;
            }
            if (blankCell(pin, cell)) {
                blanks += 1;
                continue;
            }
            if (first) try e.wrapInto(blanks == 0 and cell.wide == .wide);
            first = false;
            try e.skip(blanks);
            blanks = 0;
            try e.writeCell(pin, cell);
        }

        if (!r.wrap) {
            e.advance = 1;
            return;
        }
        // End in the last column, so the next character wraps.
        if (first) try e.wrapInto(false);
        if (blanks > 0) {
            try e.skip(blanks - 1);
            try e.plain();
            try e.w.writeByte(' ');
        }
        e.wait = .pending;
    }

    /// Before the first thing written on a row that continues a wrapped
    /// row: makes the terminal start the row. The character that wraps may
    /// scroll the screen, and a new row takes the current background
    /// colour (BCE), so the wrap is made with a plain space, and the
    /// cursor steps back over it.
    fn wrapInto(e: *RowEncoder, wide_first: bool) Error!void {
        switch (e.wait) {
            .none => return,
            .head => {
                try e.plain();
                if (wide_first) {
                    // An ideographic space is wide too, so it makes the
                    // spacer that the wide character needs.
                    try e.w.writeAll("\u{3000}\x1b[2D");
                    e.wait = .none;
                    return;
                }
                // Fill the spacer's column instead.
                try e.w.writeByte(' ');
            },
            .pending => try e.plain(),
        }
        try e.w.writeAll(" \x1b[D");
        e.wait = .none;
    }

    /// Moves past n blank cells in the row, leaving them empty.
    fn skip(e: *RowEncoder, n: usize) Error!void {
        if (n == 0) return;
        if (n == 1) try e.w.writeAll("\x1b[C") else try e.w.print("\x1b[{d}C", .{n});
    }

    fn writeCell(e: *RowEncoder, pin: vt.Pin, c: *const vt.Cell) Error!void {
        const st = cellStyle(pin, c);
        if (!st.eql(e.style)) {
            try e.w.print("{f}", .{st.formatterVt()});
            e.style = st;
        }
        try e.setLink(cellLink(pin, c));

        const has_text = (c.content_tag == .codepoint or c.content_tag == .codepoint_grapheme) and
            c.hasText();
        if (!has_text) {
            // A cell with only a colour, or only a style.
            try e.w.writeByte(' ');
            return;
        }
        try e.w.printUnicodeCodepoint(c.codepoint());
        if (c.content_tag == .codepoint_grapheme) {
            if (pin.grapheme(c)) |extra| {
                for (extra) |cp| try e.w.printUnicodeCodepoint(cp);
            }
        }
    }

    fn setLink(e: *RowEncoder, link: ?Link) Error!void {
        if (linkEql(e.link, link)) return;
        if (e.link != null) try e.w.writeAll("\x1b]8;;\x1b\\");
        if (link) |l| {
            if (l.id) |id| {
                try e.w.print("\x1b]8;id={s};{s}\x1b\\", .{ id, l.uri });
            } else {
                try e.w.print("\x1b]8;;{s}\x1b\\", .{l.uri});
            }
        }
        e.link = link;
    }

    /// Resets the pen and closes any hyperlink, before line breaks and
    /// filler spaces.
    fn plain(e: *RowEncoder) Error!void {
        if (!e.style.default()) {
            try e.w.writeAll("\x1b[0m");
            e.style = .{};
        }
        try e.setLink(null);
    }

    fn finish(e: *RowEncoder) Error!void {
        try e.plain();
    }
};

fn linkEql(a: ?RowEncoder.Link, b: ?RowEncoder.Link) bool {
    const x = a orelse return b == null;
    const y = b orelse return false;
    if (!std.mem.eql(u8, x.uri, y.uri)) return false;
    if (x.id == null or y.id == null) return x.id == null and y.id == null;
    return std.mem.eql(u8, x.id.?, y.id.?);
}

/// The style a cell is drawn with, including a cell that only carries a
/// background colour.
pub fn cellStyle(pin: vt.Pin, cell: *const vt.Cell) vt.Style {
    return switch (cell.content_tag) {
        .codepoint, .codepoint_grapheme => pin.style(cell),
        .bg_color_palette => .{ .bg_color = .palette(cell.content.color_palette.data) },
        .bg_color_rgb => .{ .bg_color = .rgb(.{
            .r = cell.content.color_rgb.r,
            .g = cell.content.color_rgb.g,
            .b = cell.content.color_rgb.b,
        }) },
    };
}

/// The cell's hyperlink. Implicit IDs are left out: the terminal that
/// reads the snapshot makes its own.
pub fn cellLink(pin: vt.Pin, cell: *const vt.Cell) ?RowEncoder.Link {
    if (!cell.hyperlink) return null;
    const page = pin.node.page();
    const id = page.lookupHyperlink(cell) orelse return null;
    const entry = page.hyperlink_set.get(page.memory, id);
    return .{
        .id = switch (entry.id) {
            .explicit => |v| v.slice(page.memory),
            .implicit => null,
        },
        .uri = entry.uri.slice(page.memory),
    };
}

/// A cell the snapshot need not write: no text but a space, no style, no
/// hyperlink.
pub fn blankCell(pin: vt.Pin, cell: *const vt.Cell) bool {
    switch (cell.content_tag) {
        .codepoint => {},
        .codepoint_grapheme, .bg_color_palette, .bg_color_rgb => return false,
    }
    if (cell.hyperlink) return false;
    if (cell.wide != .narrow) return cell.wide == .spacer_tail;
    if (cell.hasText() and cell.codepoint() != ' ') return false;
    return cellStyle(pin, cell).default();
}

fn writeActive(screen: *const vt.Screen, w: *std.Io.Writer) Error!void {
    const pages = &screen.pages;
    const top = pages.pin(.{ .active = .{ .x = 0, .y = 0 } }) orelse return;
    const bottom = pages.pin(.{ .active = .{
        .x = pages.cols - 1,
        .y = pages.rows - 1,
    } }) orelse return;
    _ = try writeRows(w, top, bottom);
}

/// Everything after the text, for the active screen.
fn writeState(term: *vt.Terminal, w: *std.Io.Writer) Error!void {
    const screen = term.screens.active;

    try writeSavedCursor(screen, w);
    try writeModes(term, w);

    // DECSTBM and DECSLRM. Both move the cursor home, and the cursor comes
    // next. DECSLRM only applies while mode 69, written above, is on.
    const region = term.scrolling_region;
    if (region.top != 0 or region.bottom != term.rows - 1) {
        try w.print("\x1b[{d};{d}r", .{ region.top + 1, region.bottom + 1 });
    }
    if (region.left != 0 or region.right != term.cols - 1) {
        try w.print("\x1b[{d};{d}s", .{ region.left + 1, region.right + 1 });
    }

    // Origin mode moves the cursor home too, so it also goes before it.
    const origin = term.modes.get(.origin);
    if (origin) try writeMode(w, .origin, true);

    // The cursor position (including a pending wrap), the pen, an open
    // hyperlink, protection and the character sets.
    var f: vt.formatter.ScreenFormatter = .init(screen, .{ .emit = .vt });
    f.content = .none;
    f.extra = .{
        .cursor = true,
        .style = true,
        .hyperlink = true,
        .protection = true,
        .kitty_keyboard = false,
        .charsets = true,
    };
    if (origin) f.cursor_origin = .{ .x = region.left, .y = region.top };
    try f.format(w);

    try writeKitty(screen, w);
    if (term.flags.modify_other_keys_2) try w.writeAll("\x1b[>4;2m");

    // DECSCUSR, when the program chose a cursor shape.
    if (!term.cursor.is_default) {
        const blink = term.modes.get(.cursor_blinking);
        const n: u8 = switch (screen.cursor.cursor_style) {
            .block, .block_hollow => if (blink) 1 else 2,
            .underline => if (blink) 3 else 4,
            .bar => if (blink) 5 else 6,
        };
        try w.print("\x1b[{d} q", .{n});
    }

    try writeColors(term, w);

    if (term.getTitle()) |title| {
        if (title.len > 0) try w.print("\x1b]2;{s}\x1b\\", .{title});
    }
    // getPwd leaves out the NUL that setPwd stores (zmx issue #222).
    if (term.getPwd()) |pwd| {
        if (pwd.len > 0) try w.print("\x1b]7;{s}\x1b\\", .{pwd});
    }
}

/// DECSC for the screen's saved cursor, so a later DECRC lands where the
/// program expects. This uses the cursor, so it must come before the
/// final cursor position.
fn writeSavedCursor(screen: *const vt.Screen, w: *std.Io.Writer) Error!void {
    const s = screen.saved_cursor orelse return;
    try writeCursorAt(w, s.x, s.y);
    const st = s.style;
    try w.print("{f}\x1b7\x1b[0m", .{st.formatterVt()});
}

fn writeCursorAt(w: *std.Io.Writer, x: usize, y: usize) Error!void {
    try w.print("\x1b[{d};{d}H", .{ y + 1, x + 1 });
}

/// Modes that this file writes in a set place, or never writes.
fn skipMode(comptime mode: vt.Mode) bool {
    return switch (mode) {
        // The alternate screen is entered on purpose, between the screens.
        .alt_screen_legacy,
        .alt_screen,
        .alt_screen_save_cursor_clear_enter,
        // 1048 saves or restores the cursor; it is an action, not a state.
        .save_cursor,
        // Replaying synchronised output stalls the client (zmx).
        .synchronized_output,
        // DECCOLM resizes or clears the screen.
        .@"132_column",
        // Written later, as it moves the cursor.
        .origin,
        // Written first, before the text.
        .grapheme_cluster,
        // Written from the mouse flags, in the right order.
        .mouse_event_x10,
        .mouse_event_normal,
        .mouse_event_button,
        .mouse_event_any,
        .mouse_format_utf8,
        .mouse_format_sgr,
        .mouse_format_urxvt,
        .mouse_format_sgr_pixels,
        => true,
        else => false,
    };
}

/// Every mode that differs from its default, then the mouse modes.
pub fn writeModes(term: *const vt.Terminal, w: *std.Io.Writer) Error!void {
    const info = @typeInfo(vt.Mode).@"enum";
    inline for (info.field_names, info.field_values) |name, value| {
        const mode: vt.Mode = @fromBackingInt(@intCast(value));
        if (comptime !skipMode(mode)) {
            const current = term.modes.get(mode);
            if (current != @field(term.modes.default, name)) {
                try writeMode(w, mode, current);
            }
        }
    }

    // The modes keep every mouse mode the program set, but only the last
    // one counts. The flags hold that one.
    switch (term.flags.mouse_event) {
        .none => {},
        .x10 => try w.writeAll("\x1b[?9h"),
        .normal => try w.writeAll("\x1b[?1000h"),
        .button => try w.writeAll("\x1b[?1002h"),
        .any => try w.writeAll("\x1b[?1003h"),
    }
    switch (term.flags.mouse_format) {
        .x10 => {},
        .utf8 => try w.writeAll("\x1b[?1005h"),
        .sgr => try w.writeAll("\x1b[?1006h"),
        .urxvt => try w.writeAll("\x1b[?1015h"),
        .sgr_pixels => try w.writeAll("\x1b[?1016h"),
    }
}

fn writeMode(w: *std.Io.Writer, mode: vt.Mode, on: bool) Error!void {
    const tag: vt.modes.ModeTag = @bitCast(@backingInt(mode));
    try w.print("\x1b[{s}{d}{s}", .{
        if (tag.ansi) "" else "?",
        tag.value,
        if (on) "h" else "l",
    });
}

/// The kitty keyboard flags as a stack: the base entry with CSI = u, then
/// each pushed entry with CSI > u. A program that pops later gets back
/// what it pushed over.
fn writeKitty(screen: *const vt.Screen, w: *std.Io.Writer) Error!void {
    const stack = screen.kitty_keyboard;
    const base = stack.flags[0].int();
    if (base != 0) try w.print("\x1b[={d};1u", .{base});
    var i: usize = 1;
    while (i <= stack.idx) : (i += 1) {
        try w.print("\x1b[>{d}u", .{stack.flags[i].int()});
    }
}

fn writeColors(term: *const vt.Terminal, w: *std.Io.Writer) Error!void {
    const colors = &term.colors;
    var it = colors.palette.mask.iterator(.{});
    while (it.next()) |i| {
        const c = colors.palette.current[i];
        try w.print("\x1b]4;{d};rgb:{x:0>2}/{x:0>2}/{x:0>2}\x1b\\", .{ i, c.r, c.g, c.b });
    }
    const dynamic = [_]struct { u8, vt.color.DynamicRGB }{
        .{ 10, colors.foreground },
        .{ 11, colors.background },
        .{ 12, colors.cursor },
    };
    for (dynamic) |d| {
        const c = d[1].override orelse continue;
        try w.print("\x1b]{d};rgb:{x:0>2}/{x:0>2}/{x:0>2}\x1b\\", .{ d[0], c.r, c.g, c.b });
    }
}
