"""Read-only native observations for the original runner's owned VM rows.

Rows select diagnostic queries only. They never authorise exec, deletion,
restart, cgroup writes or any adoption of a native process owner.
"""
import json, os, re, shlex

HOST_SAMPLE = r'''
import json,os,pathlib,subprocess,sys
unit=sys.argv[1]
def properties():
    out=subprocess.check_output(['systemctl','--user','show',unit,'-p','ControlGroup','-p','InvocationID','-p','LoadState','-p','MemoryMax','-p','MemorySwapMax'],timeout=10,text=True)
    return dict(line.split('=',1) for line in out.splitlines() if '=' in line)
before=properties()
if before.get('LoadState')=='not-found' or not before.get('ControlGroup'):
    print(json.dumps({'unit_absent':True,'properties':before}));sys.exit(0)
group=before['ControlGroup']
if not group.startswith('/') or '..' in group.split('/'):
    raise RuntimeError('invalid diagnostic cgroup path')
root=pathlib.Path('/sys/fs/cgroup'+group)
result={'unit_absent':False,'properties':before,'processes':[],'memory_stat':{},'sealed_runtime_bytes':0}
sealed={}
result['memory_current']=int((root/'memory.current').read_text())
for line in (root/'memory.stat').read_text().splitlines():
    k,v=line.split();result['memory_stat'][k]=int(v)
for leaf in root.rglob('cgroup.procs'):
    for word in leaf.read_text().split():
        pid=int(word);proc=pathlib.Path('/proc')/word
        try:
            stat=proc.joinpath('stat').read_text();birth=stat[stat.rfind(')')+2:].split()[19]
            status=dict(line.split(':',1) for line in proc.joinpath('status').read_text().splitlines() if ':' in line)
            name=status['Name'].strip();rss=int(status.get('VmRSS','0 kB').split()[0])*1024
            rollup=dict(line.split(':',1) for line in proc.joinpath('smaps_rollup').read_text().splitlines() if ':' in line)
            anonymous_pss=int(rollup.get('Pss_Anon','0 kB').split()[0])*1024
            words=proc.joinpath('cmdline').read_bytes().split(b'\0')
            role='vmm' if 'cloud-hyper' in name or 'firecracker' in name or b'chv-run' in words or b'chv-exec' in words else 'supervisor' if b'supervise' in words else 'egress' if b'egress' in words else 'other'
            for descriptor in proc.joinpath('fd').iterdir():
                target=os.readlink(descriptor)
                if target.startswith('/memfd:runtime-vmm') or target.startswith('/memfd:runtime-kernel'):
                    file=descriptor.stat();sealed[(file.st_dev,file.st_ino)]=file.st_size
            guest_rss=guest_size=0
            mapping=False
            for line in proc.joinpath('smaps').read_text().splitlines():
                if '-' in line.split()[0]:mapping='memfd:ch_ram' in line
                elif mapping and line.startswith('Rss:'):guest_rss+=int(line.split()[1])*1024
                elif mapping and line.startswith('Size:'):guest_size+=int(line.split()[1])*1024
            current=proc.joinpath('stat').read_text();now=current[current.rfind(')')+2:].split()[19]
            if now!=birth:raise RuntimeError('diagnostic process changed during sample')
            result['processes'].append({'pid':pid,'birth_ticks':birth,'name':name,'role':role,'rss':rss,'anonymous_pss':anonymous_pss,'guest_ram_rss':guest_rss,'guest_ram_mapping_size':guest_size})
        except (OSError,KeyError,ValueError) as error:
            result['processes'].append({'pid':pid,'error':str(error)})
after=properties()
if before!=after:raise RuntimeError('native unit invocation changed during the diagnostic sample')
result['guest_ram_rss']=sum(p.get('guest_ram_rss',0) for p in result['processes'])
result['guest_ram_mapping_size']=sum(p.get('guest_ram_mapping_size',0) for p in result['processes'])
result['sealed_runtime_bytes']=sum(sealed.values())
if result['guest_ram_mapping_size']!=1073741824:result['mapping_error']='original guest RAM mapping was not completely observed'
else:
    result['outside_guest']=result['memory_current']-result['guest_ram_rss']
    result['parts']={role:sum(p.get('rss',0)-p.get('guest_ram_rss',0) for p in result['processes'] if p.get('role')==role) for role in ('vmm','supervisor','egress','other')}
    result['parts']['pagetables']=result['memory_stat'].get('pagetables',0)+result['memory_stat'].get('sec_pagetables',0)
    result['parts']['sealed_runtime']=result['sealed_runtime_bytes']
    result['parts']['residual']=result['outside_guest']-sum(p.get('anonymous_pss',0) for p in result['processes'])-result['parts']['pagetables']-result['parts']['sealed_runtime']
print(json.dumps(result))
'''


def sample(c,sid,require_live=True):
    if not c.exact(sid,"read-only memory measurement"):
        raise RuntimeError("memory measurement has no exact owned session")
    unit=c.NAME+"-s-"+sid+".service"
    result=c.host_ssh("python3 - %s" % shlex.quote(unit),HOST_SAMPLE.encode())
    value=json.loads(result.stdout)
    if require_live and value.get("unit_absent"):
        raise RuntimeError("the measured unit is absent")
    return value


def disk(c,sid):
    if sid not in c.created or not re.fullmatch(r"[A-Za-z0-9_-]{1,64}",sid):
        raise RuntimeError("disk measurement needs a fresh owned ID")
    path='"${XDG_STATE_HOME:-$HOME/.local/state}/%s/sessions/%s/vm/disk.img"' % (c.NAME,sid)
    raw=c.host_ssh("stat -c '%s %b %B' " + path).stdout.decode().split()
    if len(raw)!=3 or not all(v.isdigit() for v in raw): raise RuntimeError("the actual sparse session disk could not be measured")
    return {"size_bytes":int(raw[0]),"allocated_bytes":int(raw[1])*int(raw[2])}
