"""Run the real socket-pair component benchmarks in owned capped sessions."""
import os,re,time

import boundary,conformance_evidence,owned


def run(c,evidence):
    boundary.require_disposable(c)
    for kind,variable,pattern in (('link','E2E_LINK_BENCH','BenchmarkThroughput'),('supervisor','E2E_SUPERVISOR_BENCH','BenchmarkRelayThroughput')):
        source=os.path.abspath(os.environ[variable])
        if os.path.islink(source) or not os.path.isfile(source) or os.path.getsize(source)>256<<20:
            raise ValueError('the selected native benchmark binary is linked or exceeds its bound')
        staged=owned.run_dir()+'/throughput-'+kind
        evidence.action('stage_benchmark_fixture',file='throughput-'+kind)
        with open(source,'rb') as stream:c.host_ssh('cat > '+staged,stream.read())
        c.host_ssh('chmod 700 '+staged)
        script='sleep 3; exec $HOME/.e2e-run-'+owned.RUN+'/throughput-'+kind+" -test.run '^$' -test.bench '^"+pattern+"$' -test.benchtime 1024x -test.count 3 -test.cpu 1 -test.timeout 10m"
        name=owned.e2e_name('e2e-throughput-'+kind)
        evidence.action('start_attempt',name=name,wall='none',memory_max_bytes=512<<20)
        sid,_,err=owned.start_new(c,c.envx,c.created,name,'--wall','none','--downgrade','--memory-max','512MiB','--',*owned.program('/bin/sh',script))
        if not sid:raise RuntimeError('the actual capped throughput process did not start: '+owned.safe_text(err))
        c.names[sid]=name
        evidence.action('start_confirmed',id=sid,name=name)
        term=c.attach(sid)
        try:
            until=time.monotonic()+900
            while time.monotonic()<until:
                term.read(.1)
                row=c.session_rows().get(sid,{})
                if row.get('state') in owned.ENDED:break
                if len(term.buf)>2<<20:raise RuntimeError('the actual benchmark output exceeded its bound')
            else:raise RuntimeError('the bounded actual benchmark did not end')
            term.read(.5)
            if row.get('state')!='exited' or row.get('exit_code',0)!=0:raise RuntimeError('the actual benchmark failed, so throughput is inconclusive')
            data=re.sub(rb'\x1b\[[0-?]*[ -/]*[@-~]',b'',term.buf)
            evidence.file(kind+'-throughput.txt',data)
            samples=conformance_evidence.throughput_samples(data.decode('utf-8'),kind)
            evidence.data.setdefault('throughput',{})[kind]=samples
            evidence.save()
        finally:
            evidence.action('delete_attempt',id=sid)
            term.close()
            pending=[sid]
            if owned.clean_up(c,pending):raise RuntimeError('the original benchmark session could not be deleted')
            if sid in c.created:c.created.remove(sid)
            c.names.pop(sid,None)
