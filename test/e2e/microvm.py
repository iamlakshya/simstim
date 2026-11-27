"""Real CLI conformance on an authenticated disposable Linux KVM host.

This module starts no providers, changes no root prerequisites and opens no
listener. A selected run fails when its hardware or lifecycle is unavailable.
The existing run.py owns the root, host lock, fresh IDs and final cleanup.
"""
import collections, hashlib, json, os, re, tarfile, time

import boundary, conformance_evidence, microvm_memory, microvm_trace, microvm_throughput, owned

FIXTURE = (b"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ "
           b"\x1b[38;2;255;100;0mtruecolor\x1b[0m \x1b[4:3mcurly\x1b[0m "
           b"\x1b]52;c;aGVsbG8=\x1b\\\x1b[?2026h\x1b[?2026l END")
HOST_FACTS = "printf '%s\\n' \"$HOME\" \"${XDG_RUNTIME_DIR:-/run/user/$(id -u)}\"; readlink /proc/self/ns/pid; cat /proc/sys/kernel/random/boot_id"
SHELL_PROMPT_COMMAND = b"printf 'SHELL-%s-%s\\n' \"$((6*7))\" \"$(id -u)\"\r"


def run(c, measurements=False):
    boundary.require_disposable(c)
    wall = os.environ.get("E2E_WALL", "microvm")
    evidence = conformance_evidence.Evidence(os.environ["E2E_EVIDENCE_DIR"], wall,
                                             os.environ["E2E_SUBSTRATE"], os.environ["E2E_REVISION"])
    c.conformance_evidence=evidence
    if evidence.data["client"]["os"] != "Darwin":
        raise RuntimeError("the required conformance path needs the real macOS client")
    probe = os.path.abspath(os.environ["E2E_PROBE"])
    if not os.path.isfile(probe) or os.path.islink(probe) or os.path.getsize(probe) > 32 << 20:
        raise RuntimeError("E2E_PROBE must name the built static Linux guest fixture")
    with open(probe, "rb") as stream:
        evidence.data["probe_sha256"] = hashlib.file_digest(stream, "sha256").hexdigest()
    evidence.data['client_sha256']=c.root.client_digest()
    evidence.data['client_version']=owned.json_of([c.cli,"version","--json"],c.envx)
    platform_words=c.host_ssh("uname -sm").stdout.decode().split()
    if len(platform_words)!=2 or platform_words[0]!="Linux": raise RuntimeError("the remote conformance host is not Linux")
    evidence.data["host"]={"os":platform_words[0],"arch":platform_words[1]}
    try:
        sid = start(c, probe, wall, "main")
        if not check(c, evidence, "start", sid is not None):
            return
        row = exact_row(c, sid)
        if not check(c, evidence, "requested_wall", row.get("wall", {}).get("level") == wall, row):
            return
        fidelity(c, sid, evidence)
        if measurements:
            time.sleep(60)
            term=attached(c,sid)
            try:
                evidence.sample("guest_memory",event(term,b"memory","memory"))
                evidence.sample("infrastructure_memory",microvm_memory.sample(c,sid))
                detach(term)
            finally:term.close()
            microvm_trace.measure(c,sid,probe,evidence)
            microvm_throughput.run(c,evidence)
        escapes(c, sid, evidence)
        reconnect(c, sid, evidence)
        lifecycle(c, sid, evidence)
        compat(c,probe,wall,evidence)
        disk(c, probe, wall, evidence)
        if measurements:
            measure(c, probe, wall, evidence)
        remaining=owned.clean_up(c,c.created)
        check(c,evidence,'cleanup',not remaining,{'remaining_owned_ids':remaining})
        evidence.data["suite_finished"] = True
    finally:
        evidence.save()


def check(c, evidence, name, passed, detail=None):
    evidence.check(name, passed, detail)
    return c.ok(passed, "microvm: " + name)


def start(c, probe, wall, suffix):
    name = owned.e2e_name("e2e-vm-"+suffix)
    script = "chmod 700 /work/.e2e-probe-bin; exec /work/.e2e-probe-bin serve " + owned.RUN
    c.conformance_evidence.action('start_attempt',name=name,wall=wall,copy='static-guest-probe',guest_ram_bytes=1<<30,disk_bytes=128<<20)
    sid, _, err = owned.start_new(c, c.envx, c.created, name, "--wall", wall,
                                "--copy", probe+":/work/.e2e-probe-bin", "--memory-max", "1GiB",
                                "--set", "sandbox.disk_max=128MiB", "--preset", "locked", "--",
                                *owned.program("/bin/sh", script))
    if sid:
        c.names[sid] = name
        c.conformance_evidence.action('start_confirmed',id=sid,name=name)
    else:
        c.ok(False, "microvm: fresh start failed: " + owned.safe_text(err))
    return sid


def exact_row(c, sid):
    if not c.exact(sid, "microvm check"):
        raise RuntimeError("the conformance session is no longer the run's exact owned ID")
    return c.session_rows().get(sid, {})


def event(term, command, kind, timeout=30):
    term.buf = b""
    term.send(command+b"\n")
    end = time.monotonic()+timeout
    while time.monotonic() < end:
        term.read(0.05)
        for line in term.buf.split(b"\n"):
            try:
                value = json.loads(line)
            except (ValueError, UnicodeDecodeError):
                continue
            if value.get("event") == kind:
                return value["value"]
    raise RuntimeError("the real guest did not answer " + kind)


def attached(c, sid):
    exact_row(c, sid)
    return c.attach(sid)


def detach(term):
    term.send(b"\x1c")
    code = term.exit_code(10)
    term.close()
    if code != 0:
        raise RuntimeError("the real client did not detach cleanly")


def fidelity(c, sid, evidence):
    term = attached(c, sid)
    try:
        term.read(0.2)
        term.buf = b""
        term.send(b"fidelity\n")
        check(c, evidence, "byte_exact_output", term.wait_for(rb" END", 30) and FIXTURE in term.buf)
        keys = b"\x1b[A\x1b[97;5u\x1b[200~paste\x1b[201~"
        term.buf = b""
        term.send(b"echo "+keys.hex().encode()+b"\n")
        check(c, evidence, "input_key_bytes", term.wait_for(rb">BYTES", 30) and b"BYTES<"+keys+b">BYTES" in term.buf)
        term.resize(93, 27)
        end = time.monotonic()+10
        while time.monotonic() < end and exact_row(c,sid).get("cols") != 93:
            time.sleep(0.1)
        row = exact_row(c,sid)
        check(c, evidence, "resize", row.get("cols") == 93 and row.get("rows") == 27)
        detach(term)
    finally:
        term.close()


def escapes(c, sid, evidence):
    # Every value is a read-only observation or a token-owned canary. None
    # becomes process, native-runtime or cleanup authority.
    host = c.host_ssh(HOST_FACTS).stdout.decode().splitlines()
    if len(host) != 4:
        raise RuntimeError("the host's namespace observations are incomplete")
    c.host_ssh("cat > %s/vm-canary" % owned.run_dir(), owned.NONCE.encode())
    evidence.action('host_canary_written',file='vm-canary')
    sibling = start(c, os.path.abspath(os.environ["E2E_PROBE"]), "microvm", "sibling")
    if sibling is None:
        raise RuntimeError("the socket isolation control did not start")
    args = [host[0]+"/.e2e-run-"+owned.RUN+"/vm-canary", host[1]+"/bus",
            host[1]+"/"+c.NAME+"/s/"+sibling+".sock", host[2],host[3]]
    term = attached(c,sid)
    try:
        result = event(term, ("escape "+" ".join(v.encode().hex() for v in args)).encode(), "escape")
        check(c,evidence,"escape_suite", bool(result) and all(v is True for v in result.values()), result)
        detach(term)
    finally:
        term.close()
        retire(c,sibling)


def reconnect(c, sid, evidence):
    term = attached(c,sid)
    try:
        term.buf=b""
        term.send(b"counter\n")
        if not term.wait_for(rb"N0020\n",30):
            raise RuntimeError("the reconnect fixture did not produce live output")
        evidence.action('drop_owned_client_links',id=sid)
        c.target.drop_link(term.pid)
        if not term.wait_for(rb"N0400\n",60):
            raise RuntimeError("the real attach did not recover after its own connection ended")
        counts=collections.Counter(int(v) for v in re.findall(rb"N(\d{4})\n",term.buf))
        check(c,evidence,"reconnect_exact",set(counts)==set(range(1,401)) and max(counts.values(),default=0)==1)
        detach(term)
    finally:
        term.close()


def stop(c,sid):
    exact_row(c,sid)
    address=c.HOST+"/"+sid
    c.conformance_evidence.action('stop_attempt',id=sid)
    before=time.monotonic_ns()
    code,out,err=owned.run_cmd([c.cli,"stop",address,"--json"],env=c.envx,timeout=60)
    if code!=0:
        raise RuntimeError("real stop failed: "+owned.safe_text(err))
    value=json.loads(out)
    row=exact_row(c,sid)
    if row.get("state")!="stopped" or row.get("alive"):
        raise RuntimeError("stop returned without a stopped, non-live row")
    return (time.monotonic_ns()-before)/1e6,value


def lifecycle(c,sid,evidence):
    term=attached(c,sid)
    try:
        evidence.action('write_guest_persistence_controls',id=sid)
        written=event(term,b"write","write")
        if written!={"/work":True,"/tmp":True}: raise RuntimeError("the persistence fixture did not write both controls")
        term.buf=b""; term.send(b"ping beforestop\n")
        if not term.wait_for(rb"ACK beforestop\n",30): raise RuntimeError("the old ring control did not arrive")
        detach(term)
    finally:
        term.close()
    elapsed,_=stop(c,sid)
    evidence.sample("lifecycle_stop_ms",elapsed)
    stopped=microvm_memory.sample(c,sid,require_live=False)
    check(c,evidence,"stopped_no_memory", stopped.get("unit_absent") is True,stopped)
    before=time.monotonic_ns()
    term=attached(c,sid)  # The actual stopped-row wake path, not direct spawn.
    try:
        term.read(0.2)
        previous=term.buf
        restored=event(term,b"inspect","persist",60)
        evidence.sample("lifecycle_wake_ms",(time.monotonic_ns()-before)/1e6)
        check(c,evidence,"cold_wake_files",restored.get("work") is True and restored.get("temp") is False,restored)
        check(c,evidence,"cold_wake_empty_ring",b"ACK beforestop" not in previous and b"N0400" not in previous)
        detach(term)
    finally:
        term.close()
    stop(c,sid)
    address=c.HOST+"/"+sid
    evidence.action('resume_attempt',id=sid)
    code,out,err=owned.run_cmd([c.cli,"resume",address,"--detach","--json"],env=c.envx,timeout=120)
    if not check(c,evidence,"explicit_resume",code==0 and exact_row(c,sid).get('alive') is True,owned.safe_text(err)):
        raise RuntimeError('the actual resume command did not cold-start its original selected ID')
    try:
        resumed=json.loads(out)
        if resumed.get('id')!=sid:raise RuntimeError('resume returned another session identity')
    finally:
        retire(c,sid)


def disk(c,probe,wall,evidence):
    sid=start(c,probe,wall,"disk")
    if sid is None:
        raise RuntimeError("the bounded disk fixture did not start")
    before=microvm_memory.disk(c,sid)
    cache_before=microvm_memory.sample(c,sid).get('memory_stat',{})
    term=attached(c,sid)
    try:
        evidence.action('fill_bounded_guest_disk',id=sid,disk_bytes=128<<20,maximum_write_bytes=512<<20)
        result=event(term,b"fill","disk",60)
        after=microvm_memory.disk(c,sid)
        cache_after=microvm_memory.sample(c,sid).get('memory_stat',{})
        if not {'file','shmem'}<=cache_before.keys() or not {'file','shmem'}<=cache_after.keys():raise RuntimeError('the native disk-cache counters are unavailable')
        evidence.sample('disk_cache_delta_bytes',max(0,(cache_after['file']-cache_after['shmem'])-(cache_before['file']-cache_before['shmem'])))
        check(c,evidence,"disk_enospc",result.get("enospc") is True and "cleanup_error" not in result,result)
        check(c,evidence,"bounded_host_disk",after["allocated_bytes"] <= 128*1024*1024 and after["size_bytes"]==before["size_bytes"],after)
        detach(term)
    finally:
        term.close()
        retire(c,sid)


def measure(c,probe,wall,evidence):
    runs=int(os.environ.get("E2E_RUNS","20"))
    if runs!=20:
        raise ValueError("the release measurement requires exactly 20 starts")
    for number in range(runs):
        before=time.monotonic_ns()
        sid=start(c,probe,wall,"timing%02d" % number)
        if sid is None:
            raise RuntimeError("a timed start failed")
        term=attached(c,sid)
        try:
            term.buf=b""; term.send(b"ping start\n")
            if not term.wait_for(rb"ACK start\n",60): raise RuntimeError("the actual holder/agent did not answer")
            evidence.sample("start_to_agent_ms",(time.monotonic_ns()-before)/1e6)
            detach(term)
        finally:
            term.close()
        if number==0 and os.environ.get("E2E_MEASURE_SHELL")=="1":
            shell_measurement(c,sid,evidence)
        elapsed,_=stop(c,sid)
        evidence.sample("stop_ms",elapsed)
        before=time.monotonic_ns()
        term=attached(c,sid)
        try:
            term.buf=b""; term.send(b"ping wake\n")
            if not term.wait_for(rb"ACK wake\n",60): raise RuntimeError("wake on attach gave no real agent answer")
            evidence.sample("wake_ms",(time.monotonic_ns()-before)/1e6)
            detach(term)
        finally:
            term.close()
        retire(c,sid)
    density(c,probe,wall,evidence)


def compat(c,probe,wall,evidence):
    if evidence.data["host"]["arch"] not in ("x86_64","amd64"):
        check(c,evidence,"compat_unshare",True,{"applicable":False,"arch":evidence.data["host"]["arch"]})
        return
    sid=start(c,probe,wall,"compat")
    if sid is None: raise RuntimeError("the real compatibility-ABI fixture did not start")
    term=attached(c,sid)
    try:
        term.buf=b"";term.send(b"compat\n")
        end=time.monotonic()+15
        row={}
        while time.monotonic()<end:
            term.read(.1)
            if b'"event":"compat"' in term.buf:break
            row=exact_row(c,sid)
            if not row.get("alive"):break
        claims=[]
        for line in term.buf.splitlines():
            try:claims.append(json.loads(line))
            except (ValueError,UnicodeDecodeError):pass
        denied=any(v.get("event")=="compat" and v.get("value",{}).get("denied") is True for v in claims)
        signal=str(row.get("signal","")).lower()
        denied=denied or not row.get("alive",True) and signal in ("sigsys","bad system call","sys")
        check(c,evidence,"compat_unshare",denied,{"applicable":True,"row":row,"claims":claims})
    finally:
        term.close();retire(c,sid)


def density(c,probe,wall,evidence):
    target=2 if wall=="microvm" else 3
    made=[]
    for index in range(target):
        sid=density_agent(c,wall,index)
        if sid is None: raise RuntimeError("the specified density target did not fit; no extra sessions were attempted")
        made.append(sid)
    time.sleep(60)
    for sid in made:
        term=attached(c,sid)
        try:
            if not term.read(2): raise RuntimeError("the actual idle agent did not answer attach")
            evidence.sample("host_memory",microvm_memory.sample(c,sid))
            detach(term)
        finally:
            term.close()
    check(c,evidence,"density",len(made)==target,{"target":target,"started":len(made),"guest_ram_bytes":1<<30,"agent":"claude"})
    for sid in made: retire(c,sid)


def density_agent(c,wall,index):
    name=owned.e2e_name("e2e-vm-density%d" % index)
    before=owned.sessions_on(c,c.envx)
    if before is None: raise RuntimeError("density cannot establish its fresh session IDs")
    c.conformance_evidence.action('density_start_attempt',name=name,wall=wall,agent='claude',guest_ram_bytes=1<<30,target=2 if wall=='microvm' else 3)
    code,out,err=owned.run_cmd([c.cli,"new",c.HOST,"--agent","claude","--name",name,"--wall",wall,
                               "--memory-max","1GiB","--preset","locked","--detach","--json"],env=c.envx,timeout=120)
    sid=owned.started_id(c,out,name,before) if code==0 else None
    if sid:
        owned.own(sid,c.created); c.names[sid]=name
        c.conformance_evidence.action('start_confirmed',id=sid,name=name)
    else:
        owned.leftovers(c,c.envx,name,before,c.created,"density start failed")
        c.ok(False,"microvm: actual Claude Code density start failed: "+owned.safe_text(err))
    return sid


def retire(c,sid):
    c.conformance_evidence.action('delete_attempt',id=sid)
    pending=[sid]
    if owned.clean_up(c,pending): raise RuntimeError("the owned conformance session could not be deleted")
    if sid in c.created: c.created.remove(sid)
    c.names.pop(sid,None)


def shell_measurement(c,sid,evidence):
    address=c.HOST+"/"+sid
    for number in range(20):
        before=time.monotonic_ns()
        term=attached(c,sid)
        try:
            term.buf=b"";term.send(b"ping shellbaseline\n")
            if not term.wait_for(rb"ACK shellbaseline\n",30):raise RuntimeError("plain attach gave no real answer")
            evidence.sample("plain_attach_ms",(time.monotonic_ns()-before)/1e6)
            detach(term)
        finally:term.close()
        before=time.monotonic_ns()
        term=c.Term([c.cli,"shell",address],env=c.envx)
        try:
            term.send(SHELL_PROMPT_COMMAND)
            if not term.wait_for(rb"SHELL-42-1000",30):raise RuntimeError("the real sandbox shell did not reach its prompt as agent")
            evidence.sample("shell_to_prompt_ms",(time.monotonic_ns()-before)/1e6)
            detach(term)
        finally:term.close()
