"""The real 1000-key CLI path and original debug-bundle readout."""
import json, os, re, tarfile, time

import boundary, conformance_evidence, owned


def measure(c,vm_sid,probe,evidence):
    boundary.require_disposable(c)
    evidence.action('stage_baseline_fixture',file='trace-probe')
    with open(probe,"rb") as stream:
        c.host_ssh("cat > %s/trace-probe" % owned.run_dir(),stream.read())
    c.host_ssh("chmod 700 %s/trace-probe" % owned.run_dir())
    name=owned.e2e_name("e2e-vm-latency-baseline")
    evidence.action('start_attempt',name=name,wall='none',memory_max_bytes=256<<20)
    script="exec $HOME/.e2e-run-"+owned.RUN+"/trace-probe serve "+owned.RUN
    sid,_,err=owned.start_new(c,c.envx,c.created,name,"--wall","none","--downgrade","--memory-max","256MiB","--dir",owned.run_dir(),"--",*owned.program("/bin/sh",script))
    if not sid: raise RuntimeError("the real none baseline failed: "+owned.safe_text(err))
    c.names[sid]=name
    evidence.action('start_confirmed',id=sid,name=name)
    try:
        row=c.session_rows().get(sid,{})
        if row.get('wall') and row['wall'].get('level')!='none':raise RuntimeError('the plain timing control did not retain wall none')
        baseline=keys(c,sid,evidence,"none")
        measured=keys(c,vm_sid,evidence,"microvm")
        samples=conformance_evidence.host_key_bounds(measured[0],measured[2],measured[3],measured[4])
        native=conformance_evidence.native_key_samples(baseline[0],baseline[2],baseline[4])
        upper=[row["upper_ns"] for row in samples]
        evidence.data["key_to_pty"]={"baseline_ns":native,"microvm_bounds_ns":samples,"method":"host-clock-controlled-window-upper-v1",
            "original_generation":measured[3]['generation'],"single_inflight_acks":measured[4],
            "p50_added_upper_ns":max(0,conformance_evidence.percentile(upper,.5)-conformance_evidence.percentile(native,.5)),
            "p99_added_upper_ns":max(0,conformance_evidence.percentile(upper,.99)-conformance_evidence.percentile(native,.99))}
        result=evidence.data["key_to_pty"]
        c.ok(evidence.check("key_to_pty",result["p50_added_upper_ns"]<=300000 and result["p99_added_upper_ns"]<=2000000,result),"microvm: the 1000-key added-latency release gate")
    finally:
        evidence.action('delete_attempt',id=sid)
        pending=[sid]
        if owned.clean_up(c,pending): raise RuntimeError("the baseline's own ID could not be deleted")
        if sid in c.created:c.created.remove(sid)
        c.names.pop(sid,None)


def keys(c,sid,evidence,label):
    if not c.exact(sid,"trace measurement"):raise RuntimeError("the trace has no exact owned session")
    address=c.HOST+"/"+sid
    code,_,err=owned.run_cmd([c.cli,"debug","start",address,"--for","5m"],env=c.envx,timeout=60)
    if code!=0:raise RuntimeError("the actual holder trace could not start: "+owned.safe_text(err))
    path=os.path.join(c.root.path,"trace-"+label+".jsonl")
    term=c.Term([c.cli,"--debug=attach","--debug-file",path,"attach","--id","--",address],env=c.envx)
    try:
        term.read(.2)
        acks=[]
        for index in range(1000):
            token=("key%04d" % index).encode()
            term.buf=b"";term.send(b"ping "+token+b"\n")
            until=time.monotonic()+10
            expected=b"ACK "+token+b"\n"
            while time.monotonic()<until and expected not in term.buf:term.read(.002)
            if term.buf!=expected:raise RuntimeError("the controlled key window was incomplete or held unrelated output")
            acks.append({'index':index,'token':token.decode(),'exact':True})
        before=set(os.listdir(c.root.path))
        code,_,err=owned.run_cmd([c.cli,"debug","bundle",address],env=c.envx,timeout=180)
        if code!=0:raise RuntimeError("the actual debug bundle failed: "+owned.safe_text(err))
        names=[n for n in set(os.listdir(c.root.path))-before if n.endswith(".tar.gz")]
        if len(names)!=1:raise RuntimeError("the debug command did not make exactly one original bundle")
        archive=os.path.join(c.root.path,names[0])
        with open(archive,"rb") as stream:evidence.file(label+"-debug.tar.gz",stream.read(32<<20|1))
        records,clocks,bounds=read_bundle(archive,label)
        with open(path,"rb") as stream:client_data=stream.read(32<<20|1)
        evidence.file(label+"-client.jsonl",client_data)
        client=[json.loads(line) for line in client_data.splitlines() if line]
        evidence.file(label+"-acks.json",json.dumps(acks).encode())
        return records,clocks,client,bounds,acks
    finally:
        term.send(b"\x1c");term.exit_code(10);term.close()


def read_bundle(path,label):
    return conformance_evidence.read_trace_bundle(path,label)
