//! Builds the shadow terminal.
//!
//!   zig build                 this machine, Debug, into zig-out/bin
//!   zig build linux           static Linux binaries (musl, amd64 and
//!                             arm64), ReleaseSafe, into zig-out/linux
//!
//! The tests are in Go: internal/host/vt_test.go.

const std = @import("std");
const zon = @import("build.zig.zon");

const linux_targets = [_]struct { std.Target.Query, []const u8 }{
    .{ .{ .cpu_arch = .x86_64, .os_tag = .linux, .abi = .musl }, "amd64" },
    .{ .{ .cpu_arch = .aarch64, .os_tag = .linux, .abi = .musl }, "arm64" },
};

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});
    const strip = b.option(bool, "strip", "Strip debug information") orelse
        (optimize != .debug);

    const options = b.addOptions();
    options.addOption([]const u8, "version", zon.version);
    options.addOption([]const u8, "ghostty", ghosttyCommit());

    // This machine.
    const exe = addExe(b, options, target, optimize, strip);
    b.installArtifact(exe);

    const run = b.addRunArtifact(exe);
    run.addPassthruArgs();
    b.step("run", "Run the shadow terminal").dependOn(&run.step);

    // Static Linux binaries for hosts. ReleaseSafe keeps Zig's safety
    // checks and Ghostty's assertions: a bug stops the process, and the
    // holder restarts it from its ring, instead of carrying on with a
    // corrupt screen.
    const linux = b.step("linux", "Build static Linux binaries for amd64 and arm64");
    const linux_optimize = b.option(
        std.builtin.Optimize,
        "linux-optimize",
        "Optimisation for the linux step (default ReleaseSafe)",
    ) orelse .safe;
    for (linux_targets) |t| {
        const resolved = b.resolveTargetQuery(t[0]);
        const linux_exe = addExe(b, options, resolved, linux_optimize, true);
        const install = b.addInstallArtifact(linux_exe, .{
            .dest_dir = .{ .override = .{ .custom = "linux" } },
            .dest_sub_path = b.fmt("shadow-vt-{s}", .{t[1]}),
        });
        linux.dependOn(&install.step);
    }
}

fn addExe(
    b: *std.Build,
    options: *std.Build.Step.Options,
    target: std.Build.ResolvedTarget,
    optimize: std.builtin.Optimize,
    strip: bool,
) *std.Build.Step.Compile {
    const mod = b.createModule(.{
        .root_source_file = b.path("src/main.zig"),
        .target = target,
        .optimize = optimize,
        .link_libc = true,
        .strip = strip,
    });
    mod.addOptions("build_options", options);
    mod.addImport("ghostty-vt", ghosttyModule(b, target, optimize));
    return b.addExecutable(.{ .name = "shadow-vt", .root_module = mod });
}

fn ghosttyModule(
    b: *std.Build,
    target: std.Build.ResolvedTarget,
    optimize: std.builtin.Optimize,
) *std.Build.Module {
    const dep = b.dependency("ghostty", .{
        .target = target,
        .optimize = optimize,
        .@"emit-lib-vt" = true,
        // Without these, lib-vt mode looks for Xcode on macOS.
        .@"emit-xcframework" = false,
        .@"emit-macos-app" = false,
        // The shadow terminal never shows images, so it does not keep them.
        .@"vt-features" = @as([]const u8, "-kitty-graphics"),
    });
    return dep.module("ghostty-vt");
}

/// The pinned Ghostty commit, taken from the dependency URL.
fn ghosttyCommit() []const u8 {
    const url = zon.dependencies.ghostty.url;
    const start = (std.mem.lastIndexOfScalar(u8, url, '/') orelse 0) + 1;
    const end = std.mem.indexOfPos(u8, url, start, ".") orelse url.len;
    return url[start..end];
}
