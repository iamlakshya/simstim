"""Exact conformance-only allowances, appended to the existing E2E boundary."""


def extend(table,only,modules):
    modules.extend(["microvm.py", "microvm_memory.py", "microvm_trace.py", "microvm_throughput.py"])
    for kind,values in entries(only).items():
        table[kind].update(values)


def entries(only):
    return {
        'client': {
            ("shell",): only + "measures the real shell-to-prompt path on the run's own sandbox",
            ("stop","--json"): only + "stops only the real conformance run's fresh session ID",
            ("resume","--detach","--json"): only + "cold-resumes only the run's exact stopped sandbox ID",
            ("new","--agent","claude","--name","--wall","--memory-max","1GiB","--preset","locked","--detach","--json"): only + "starts exactly the specified idle Claude Code density target",
            ("--debug=attach","--debug-file","attach","--id","--"): only + "records the 1000-key real attach into the run's private trace",
            ("debug","bundle"): only + "collects bounded original trace bytes through the actual debug bundle protocol",
        },
        'flags': {
            "--wall": only + "selects the exact real measured wall, including an explicit none baseline",
            "--copy": only + "copies the run's static Linux guest fixture through the real input protocol",
            "--set": only + "limits the conformance disk to 128 MiB",
            "--preset": only + "uses locked egress for the actual escape probes",
            "--downgrade": only + "explicitly selects the none control on this disposable host",
        },
        'sends': {
            b"ping shellbaseline\n": only + "records the real plain-attach baseline beside shell",
            "<SHELL_PROMPT_COMMAND>": only + "checks a real guest shell response as UID 1000",
            "<command + b'\\n'>": only + "sends bounded commands only to the run's actual static fixture",
            b"fidelity\n": only + "requests actual live terminal fidelity bytes",
            "<b'echo ' + keys.hex().encode() + b'\\n'>": only + "checks key encodings on the real guest PTY",
            b"counter\n": only + "starts the finite 400-record reconnect stream",
            b"ping beforestop\n": only + "marks the old ring before cold stop",
            b"ping start\n": only + "measures the real holder/agent answer",
            b"ping wake\n": only + "measures the actual wake-on-attach path",
            b"compat\n": only + "tests the real i386 compatibility ABI guard on amd64",
            "<b'ping ' + token + b'\\n'>": only + "sends exactly 1000 keyed acknowledgements to an owned fixture",
        },
        'host': {
            "'cat > ' + staged": only + "stages only a bounded selected benchmark binary inside the authenticated run folder",
            "'chmod 700 ' + staged": only + "changes only that run-owned capped benchmark fixture",
            "'uname -sm'": only + "records the real Linux kernel platform without selecting a native owner",
            "HOST_FACTS": only + "reads only the host's home/runtime names and kernel PID namespace for escape controls",
            "'cat > %s/vm-canary' % owned.run_dir()": only + "writes only the run's token-owned host canary",
            "'python3 - %s' % shlex.quote(unit)": only + "reads the exact run-owned unit's native memory observations",
            '''"stat -c '%s %b %B' " + path''': only + "reads only the fixed disk of the run's fresh session ID",
            "'cat > %s/trace-probe' % owned.run_dir()": only + "stages only the run's selected static fixture in its own host folder",
            "'chmod 700 %s/trace-probe' % owned.run_dir()": only + "makes only that run-owned baseline fixture executable",
        },
    }
