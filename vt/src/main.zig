//! The shadow terminal: a headless terminal, built on libghostty-vt, that
//! runs as a child process of a session holder. It is fed every byte the
//! session writes, and on request it writes a snapshot that redraws the
//! screen in a blank terminal. It runs in its own process so that a crash
//! here cannot take the session down.
//!
//! It speaks framed messages on stdin and stdout. See README.md.

const std = @import("std");
const build_options = @import("build_options");
const vt = @import("ghostty-vt");
const snapshot = @import("snapshot.zig");
const dump = @import("dump.zig");

const Handler = vt.TerminalStream.Handler;

/// Errors only. Ghostty warns about every sequence it does not know, and
/// stderr goes to the holder's log.
pub const std_options: std.Options = .{ .log_level = .err };

/// Frame types. Holder to vt.
const f_data = 'D';
const f_resize = 'R';
const f_snapshot = 'S';
const f_queries = 'Q';
const f_profile = 'P';
const f_dump = 'T';
const f_sync = 'Y';
/// Frame types. Vt to holder (S, T and Y reply with the same type).
const f_write = 'W';
const f_event = 'E';

/// The largest payload accepted for a frame other than D.
const max_control = 1 << 20;

/// The most columns and rows the terminal takes. A larger size, from the
/// command line or a frame, is cut to these. The holder applies the same
/// caps (internal/host/vt.go), and this process applies them too, so a size
/// that reaches it by another path cannot pass them. Ghostty allows 65535,
/// and its advisory GHSA-jqr7-69mm-v35h names apps that take a terminal
/// size from a remote client. At 4096 by 2048, this process holds under
/// 512 MiB with its scrollback full and the alternate screen on.
const max_cols = 4096;
const max_rows = 2048;

/// The one terminal this process holds.
var shadow: Shadow = undefined;

const Shadow = struct {
    alloc: std.mem.Allocator,
    term: vt.Terminal,
    stream: vt.TerminalStream,
    /// True while the holder wants replies to queries ('Q').
    answering: bool = false,
    profile: ?std.json.Parsed(Profile) = null,
    da1: []const vt.device_attributes.Primary.Feature = &.{},
};

/// What the terminal reports about itself while it answers queries.
/// Usually copied from the last client's terminal. Every field is optional.
const Profile = struct {
    /// DA1 reply: the conformance level, then the feature numbers.
    da1: ?[]const u16 = null,
    /// DA2 reply: the device type, the firmware version, the ROM number.
    da2: ?[]const u16 = null,
    /// XTVERSION reply, for example "ghostty 1.3.1".
    xtversion: ?[]const u8 = null,
    /// Default colours, as "#rrggbb", for OSC 10, 11 and 12 queries.
    fg: ?[]const u8 = null,
    bg: ?[]const u8 = null,
    cursor: ?[]const u8 = null,
    /// "dark" or "light", for CSI ? 996 n.
    color_scheme: ?[]const u8 = null,
};

pub fn main(init: std.process.Init.Minimal) !void {
    const alloc = std.heap.c_allocator;

    var cols: u16 = 80;
    var rows: u16 = 24;
    var scrollback: usize = 10_000;
    var args = init.args.iterate();
    _ = args.skip();
    while (args.next()) |arg| {
        if (std.mem.eql(u8, arg, "--version")) {
            writeOut(build_options.version ++ " ghostty " ++ build_options.ghostty ++ "\n");
            return;
        } else if (std.mem.eql(u8, arg, "--cols")) {
            cols = try parseSize(args.next() orelse return error.MissingValue, max_cols);
        } else if (std.mem.eql(u8, arg, "--rows")) {
            rows = try parseSize(args.next() orelse return error.MissingValue, max_rows);
        } else if (std.mem.eql(u8, arg, "--scrollback")) {
            scrollback = try std.fmt.parseInt(usize, args.next() orelse return error.MissingValue, 10);
        } else {
            std.log.err("unknown argument {s}", .{arg});
            return error.BadArgument;
        }
    }
    if (cols == 0) cols = 80;
    if (rows == 0) rows = 24;

    shadow = .{
        .alloc = alloc,
        .term = try vt.Terminal.init((vt.TinyIo.init).io(), alloc, .{
            .cols = cols,
            .rows = rows,
            .max_scrollback_bytes = null,
            .max_scrollback_lines = scrollback,
        }),
        .stream = undefined,
    };
    var handler: Handler = .init(&shadow.term);
    handler.effects.write_pty = null;
    handler.effects.bell = &onBell;
    handler.effects.desktop_notification = &onNotify;
    handler.effects.title_changed = &onTitle;
    handler.effects.pwd_changed = &onPwd;
    handler.effects.progress_report = &onProgress;
    handler.effects.program_status = &onStatus;
    handler.effects.device_attributes = &onDeviceAttributes;
    handler.effects.xtversion = &onXtversion;
    handler.effects.size = &onSize;
    handler.effects.color_scheme = &onColorScheme;
    shadow.stream = .init(.{ .allocator = alloc, .handler = handler });

    run() catch |err| switch (err) {
        error.EndOfStream => {},
        else => return err,
    };
}

/// Reads frames until stdin closes.
fn run() !void {
    var in: Input = .{};
    var control: std.ArrayList(u8) = .empty;
    defer control.deinit(shadow.alloc);

    while (true) {
        var header: [5]u8 = undefined;
        try in.readExact(&header);
        const kind = header[0];
        const len = std.mem.readInt(u32, header[1..5], .little);

        if (kind == f_data) {
            // Feed the bytes in pieces, straight from the read buffer.
            var left: usize = len;
            while (left > 0) {
                const chunk = try in.peek(left);
                shadow.stream.nextSlice(chunk);
                in.toss(chunk.len);
                left -= chunk.len;
            }
            continue;
        }

        if (len > max_control) {
            try in.skip(len);
            continue;
        }
        control.clearRetainingCapacity();
        try control.resize(shadow.alloc, len);
        try in.readExact(control.items);
        handleControl(kind, control.items);
    }
}

fn handleControl(kind: u8, p: []const u8) void {
    switch (kind) {
        f_resize => {
            if (p.len < 4) return;
            const cols = std.mem.readInt(u16, p[0..2], .little);
            const rows = std.mem.readInt(u16, p[2..4], .little);
            var width_px: u16 = 0;
            var height_px: u16 = 0;
            if (p.len >= 8) {
                width_px = std.mem.readInt(u16, p[4..6], .little);
                height_px = std.mem.readInt(u16, p[6..8], .little);
            }
            resize(cols, rows, width_px, height_px);
        },
        f_snapshot => {
            if (p.len < 8) return sendFrame(f_snapshot, "");
            const cols = std.mem.readInt(u16, p[0..2], .little);
            const rows = std.mem.readInt(u16, p[2..4], .little);
            const max = std.mem.readInt(u32, p[4..8], .little);
            // The snapshot is laid out at the client's size, so the screen
            // is not wrapped twice. The holder resizes the PTY to match.
            resize(cols, rows, 0, 0);
            var aw: std.Io.Writer.Allocating = .init(shadow.alloc);
            defer aw.deinit();
            snapshot.write(&shadow.term, &aw.writer, .{ .max_scrollback = max }) catch {
                return sendFrame(f_snapshot, "");
            };
            sendFrame(f_snapshot, aw.written());
        },
        f_queries => {
            shadow.answering = p.len > 0 and p[0] != 0;
            shadow.stream.handler.effects.write_pty = if (shadow.answering) &onWritePty else null;
        },
        f_profile => setProfile(p),
        f_dump => {
            const max: u32 = if (p.len >= 4) std.mem.readInt(u32, p[0..4], .little) else 0;
            var aw: std.Io.Writer.Allocating = .init(shadow.alloc);
            defer aw.deinit();
            dump.write(shadow.alloc, &shadow.term, &aw.writer, max) catch {
                return sendFrame(f_dump, "");
            };
            sendFrame(f_dump, aw.written());
        },
        // Everything sent before this frame has been handled.
        f_sync => sendFrame(f_sync, p),
        // Unknown frames are ignored, so new ones can be added.
        else => {},
    }
}

/// A size from the command line, cut to most.
fn parseSize(s: []const u8, most: u16) !u16 {
    return capSize(try std.fmt.parseInt(usize, s, 10), most);
}

/// n as a number of columns or rows: at most most.
fn capSize(n: usize, most: u16) u16 {
    return @intCast(@min(n, most));
}

fn resize(cols_asked: u16, rows_asked: u16, width_px: u16, height_px: u16) void {
    if (cols_asked == 0 or rows_asked == 0) return;
    const cols = capSize(cols_asked, max_cols);
    const rows = capSize(rows_asked, max_rows);
    const term = &shadow.term;
    if (cols == term.cols and rows == term.rows and width_px == 0) return;
    shadow.stream.handler.resize(.{
        .cols = cols,
        .rows = rows,
        .cell_size_px = if (width_px > 0 and height_px > 0) .{
            .width = width_px / cols,
            .height = height_px / rows,
        } else null,
    }) catch |err| std.log.err("resize failed: {s}", .{@errorName(err)});
}

fn setProfile(p: []const u8) void {
    // alloc_always: the strings must not point into the frame buffer,
    // which the next frame reuses.
    const parsed = std.json.parseFromSlice(Profile, shadow.alloc, p, .{
        .ignore_unknown_fields = true,
        .allocate = .alloc_always,
    }) catch |err| {
        std.log.err("bad profile: {s}", .{@errorName(err)});
        return;
    };
    if (shadow.profile) |*old| old.deinit();
    shadow.profile = parsed;
    const prof = parsed.value;

    shadow.da1 = &.{};
    if (prof.da1) |v| if (v.len > 1) {
        shadow.da1 = @ptrCast(v[1..]);
    };

    const colors = &shadow.term.colors;
    colors.foreground.default = parseColor(prof.fg);
    colors.background.default = parseColor(prof.bg);
    colors.cursor.default = parseColor(prof.cursor);
}

fn parseColor(s: ?[]const u8) ?vt.color.RGB {
    const v = s orelse return null;
    if (v.len != 7 or v[0] != '#') return null;
    const n = std.fmt.parseInt(u24, v[1..], 16) catch return null;
    return .{ .r = @truncate(n >> 16), .g = @truncate(n >> 8), .b = @truncate(n) };
}

// Effects. Each runs while the stream handles the bytes that caused it.

fn onWritePty(_: *Handler, data: []const u8) void {
    if (!shadow.answering) return;
    // The holder answers OSC 7501 support queries itself, always.
    if (std.mem.startsWith(u8, data, "\x1b]7501;")) return;
    sendFrame(f_write, data);
}

fn onDeviceAttributes(_: *Handler) vt.device_attributes.Attributes {
    var attrs: vt.device_attributes.Attributes = .{};
    // Ghostty's own replies, unless the profile says otherwise.
    attrs.secondary = .{ .device_type = .vt220, .firmware_version = 10, .rom_cartridge = 0 };
    const prof = if (shadow.profile) |p| p.value else return attrs;
    if (prof.da1) |v| if (v.len > 0) {
        attrs.primary = .{ .conformance_level = @fromBackingInt(@intCast(v[0])), .features = shadow.da1 };
    };
    if (prof.da2) |v| if (v.len >= 3) {
        attrs.secondary = .{
            .device_type = @fromBackingInt(@intCast(v[0])),
            .firmware_version = v[1],
            .rom_cartridge = v[2],
        };
    };
    return attrs;
}

fn onXtversion(_: *Handler) []const u8 {
    const prof = if (shadow.profile) |p| p.value else return "";
    return prof.xtversion orelse "";
}

fn onSize(h: *Handler) ?vt.size_report.Size {
    const t = h.terminal;
    return .{
        .rows = t.rows,
        .columns = t.cols,
        .cell_width = if (t.cols > 0) t.width_px / t.cols else 0,
        .cell_height = if (t.rows > 0) t.height_px / t.rows else 0,
    };
}

fn onColorScheme(_: *Handler) ?vt.device_status.ColorScheme {
    const prof = if (shadow.profile) |p| p.value else return null;
    const v = prof.color_scheme orelse return null;
    if (std.mem.eql(u8, v, "dark")) return .dark;
    if (std.mem.eql(u8, v, "light")) return .light;
    return null;
}

fn onBell(_: *Handler) void {
    sendFrame(f_event, "{\"type\":\"bell\"}");
}

fn onNotify(_: *Handler, n: vt.StreamAction.ShowDesktopNotification) void {
    var e = Event.begin("notify") orelse return;
    defer e.deinit();
    e.field("title", n.title);
    e.field("body", n.body);
    e.send();
}

fn onTitle(h: *Handler) void {
    var e = Event.begin("title") orelse return;
    defer e.deinit();
    e.field("title", h.terminal.getTitle() orelse "");
    e.send();
}

fn onPwd(h: *Handler) void {
    var e = Event.begin("pwd") orelse return;
    defer e.deinit();
    e.field("data", h.terminal.getPwd() orelse "");
    e.send();
}

fn onProgress(_: *Handler, r: vt.osc.Command.ProgressReport) void {
    var e = Event.begin("progress") orelse return;
    defer e.deinit();
    e.field("state", @tagName(r.state));
    const value: i16 = if (r.progress) |v| v else -1;
    e.aw.writer.print(",\"value\":{d}", .{value}) catch {};
    e.send();
}

fn onStatus(_: *Handler, r: vt.osc.Command.ProgramStatus.Report) void {
    var e = Event.begin("status") orelse return;
    defer e.deinit();
    e.field("state", @tagName(r.state));
    e.field("data", r.data);
    e.send();
}

/// A small JSON object for an E frame.
const Event = struct {
    aw: std.Io.Writer.Allocating,

    fn begin(kind: []const u8) ?Event {
        var e: Event = .{ .aw = .init(shadow.alloc) };
        e.aw.writer.print("{{\"type\":\"{s}\"", .{kind}) catch {
            e.aw.deinit();
            return null;
        };
        return e;
    }

    fn field(e: *Event, name: []const u8, value: []const u8) void {
        e.aw.writer.print(",\"{s}\":", .{name}) catch return;
        dump.writeString(&e.aw.writer, value) catch return;
    }

    fn send(e: *Event) void {
        e.aw.writer.writeByte('}') catch return;
        sendFrame(f_event, e.aw.written());
    }

    fn deinit(e: *Event) void {
        e.aw.deinit();
    }
};

// Frame input and output on stdin and stdout.

const Input = struct {
    buf: [64 * 1024]u8 = undefined,
    start: usize = 0,
    end: usize = 0,

    fn fill(in: *Input) !void {
        if (in.start == in.end) {
            in.start = 0;
            in.end = 0;
        }
        const n = try std.posix.read(0, in.buf[in.end..]);
        if (n == 0) return error.EndOfStream;
        in.end += n;
    }

    /// Returns up to `max` buffered bytes, reading more if none are buffered.
    fn peek(in: *Input, max: usize) ![]const u8 {
        if (in.start == in.end) try in.fill();
        const n = @min(max, in.end - in.start);
        return in.buf[in.start .. in.start + n];
    }

    fn toss(in: *Input, n: usize) void {
        in.start += n;
    }

    fn readExact(in: *Input, dst: []u8) !void {
        var got: usize = 0;
        while (got < dst.len) {
            const chunk = try in.peek(dst.len - got);
            @memcpy(dst[got .. got + chunk.len], chunk);
            in.toss(chunk.len);
            got += chunk.len;
        }
    }

    fn skip(in: *Input, len: usize) !void {
        var left = len;
        while (left > 0) {
            const chunk = try in.peek(left);
            in.toss(chunk.len);
            left -= chunk.len;
        }
    }
};

fn sendFrame(kind: u8, payload: []const u8) void {
    var header: [5]u8 = undefined;
    header[0] = kind;
    std.mem.writeInt(u32, header[1..5], @intCast(payload.len), .little);
    writeOut(&header);
    writeOut(payload);
}

/// Writes all of `bytes` to stdout. If the holder has gone, there is
/// nothing left to do, so the process exits.
fn writeOut(bytes: []const u8) void {
    var rest = bytes;
    while (rest.len > 0) {
        const rc = std.c.write(1, rest.ptr, rest.len);
        if (rc < 0) {
            if (std.posix.errno(rc) == .INTR) continue;
            std.process.exit(0);
        }
        rest = rest[@intCast(rc)..];
    }
}
