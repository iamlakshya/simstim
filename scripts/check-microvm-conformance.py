#!/usr/bin/env python3
"""Check actual paired evidence without changing a default or granting authority.

The JSON describes observations. It is not a native permission, a measured
result when the runner was not executed, or a replacement for CI/race gates.
"""
import argparse, hashlib, json, math, pathlib, re, sys

ROOT=pathlib.Path(__file__).resolve().parent.parent
sys.path.insert(0,str(ROOT/'test/e2e'))
from conformance_evidence import percentile,host_key_bounds,native_key_samples,read_trace_bundle,throughput_samples

REQUIRED={"start","requested_wall","byte_exact_output","input_key_bytes","resize","escape_suite",
          "compat_unshare","reconnect_exact","stopped_no_memory","cold_wake_files","cold_wake_empty_ring",
          "disk_enospc","bounded_host_disk","density","key_to_pty","explicit_resume","cleanup","runner_cleanup"}


def read(path,revision):
    root=path.parent
    if path.is_symlink() or path.stat().st_size>16<<20:raise ValueError('the report is linked or exceeds its diagnostic bound')
    data=json.loads(path.read_text())
    if type(data.get('v')) is not int or data.get('v')!=1 or data.get('revision')!=revision or data.get('wall')!='microvm' or data.get('complete') is not True:
        raise ValueError('the evidence is incomplete or belongs to another built revision')
    if data.get('client',{}).get('os')!='Darwin' or data.get('host',{}).get('os')!='Linux':
        raise ValueError('the report did not exercise macOS client to Linux host')
    if not re.fullmatch(r'[0-9a-f]{64}',data.get('client_sha256','')) or not re.fullmatch(r'[0-9a-f]{64}',data.get('probe_sha256','')) or not isinstance(data.get('client_version'),dict):
        raise ValueError('the actual client/fixture bytes and helper version were not recorded')
    for row in data.get('files',[]):
        if not re.fullmatch(r'[a-z0-9][a-z0-9._-]{0,100}',row.get('name','')):
            raise ValueError('invalid evidence filename')
        file=root/row['name']
        if file.is_symlink() or file.stat().st_size!=row['size'] or hashlib.sha256(file.read_bytes()).hexdigest()!=row['sha256']:
            raise ValueError('an original evidence file is absent or changed')
    checks=data.get('checks',{})
    if not REQUIRED<=checks.keys() or any(checks[k].get('passed') is not True for k in REQUIRED):
        raise ValueError('a required real conformance check did not pass')
    density=checks['density']['detail']
    if density.get('target')!=2 or density.get('started')!=2 or density.get('guest_ram_bytes')!=1<<30 or density.get('agent')!='claude':
        raise ValueError('density did not use exactly two idle Claude Code sessions with 1 GiB guests')
    keys=data.get('key_to_pty',{})
    if keys.get('method')!='host-clock-controlled-window-upper-v1' or not keys.get('original_generation') or len(keys.get('single_inflight_acks',[]))!=1000:
        raise ValueError('key timing has no original native generation and controlled host-clock bound')
    if len(keys.get('baseline_ns',[]))<1000 or len(keys.get('microvm_bounds_ns',[]))<1000:
        raise ValueError('the release needs 1000 real joined key samples on each wall')
    named={row['name'] for row in data.get('files',[])}
    if not {'none-debug.tar.gz','microvm-debug.tar.gz','microvm-client.jsonl','microvm-acks.json','none-client.jsonl','none-acks.json'}<=named:
        raise ValueError('the raw artifacts needed to recompute the conservative gate were not retained')
    native,_,_=read_trace_bundle(root/'none-debug.tar.gz','none')
    guest,_,bounds=read_trace_bundle(root/'microvm-debug.tar.gz','microvm')
    client=[json.loads(line) for line in (root/'microvm-client.jsonl').read_text().splitlines() if line]
    acks=json.loads((root/'microvm-acks.json').read_text())
    recomputed=host_key_bounds(guest,client,bounds,acks)
    native_client=[json.loads(line) for line in (root/'none-client.jsonl').read_text().splitlines() if line]
    baseline=native_key_samples(native,native_client,json.loads((root/'none-acks.json').read_text()))
    if baseline!=keys['baseline_ns'] or recomputed!=keys['microvm_bounds_ns'] or bounds['generation']!=keys['original_generation']:
        raise ValueError('the reported key summary disagrees with the original raw writers')
    upper=[v['upper_ns'] for v in recomputed]
    p50=max(0,percentile(upper,.5)-percentile(baseline,.5))
    p99=max(0,percentile(upper,.99)-percentile(baseline,.99))
    if p50>300000 or p99>2000000:
        raise ValueError('the added key-to-PTY latency exceeds p50 0.3 ms or p99 2 ms; release blocked')
    data['_directory']=str(root)
    return data


def findings(data):
    samples=data.get('samples',{})
    starts=samples.get('start_to_agent_ms',[])
    wakes=samples.get('wake_ms',[])
    stops=samples.get('stop_ms',[])
    if len(starts)!=20 or len(wakes)!=20 or len(stops)!=20:
        raise ValueError('start, wake and stop need 20 complete actual iterations')
    shells=samples.get('shell_to_prompt_ms',[])
    attaches=samples.get('plain_attach_ms',[])
    if len(shells)!=20 or len(attaches)!=20:
        raise ValueError('shell-to-prompt needs 20 actual shell and plain-attach runs')
    target=1500 if data['substrate']=='bare-metal' else 4000
    misses=[]
    for kind in ('link','supervisor'):
        actual=throughput_samples((pathlib.Path(data['_directory'])/(kind+'-throughput.txt')).read_text(),kind)
        if actual!=data.get('throughput',{}).get(kind):raise ValueError('the actual throughput log disagrees with its summary')
        for name,values in actual.items():
            if min(values)<100:misses.append({'metric':kind+'_throughput_'+name.replace('/','_'),'measured':min(values),'limit':100})
    shell_added=max(0,percentile(shells,.5)-percentile(attaches,.5))
    if shell_added>300:misses.append({'metric':'shell_added_ms','measured':shell_added,'limit':300})
    for label,measured,limit in [('cached_start_ms',percentile(starts,.5),target),('wake_ms',percentile(wakes,.5),percentile(starts,.5)+500),('stop_ms',percentile(stops,.95),5000)]:
        if measured>limit:misses.append({'metric':label,'measured':measured,'limit':limit})
    guests=samples.get('guest_memory',[])
    hosts=samples.get('infrastructure_memory',[])
    if not guests or not hosts:
        raise ValueError('the 60-second guest and native memory split was not measured')
    for guest in guests:
        if guest.get('claim') is not True or any(type(guest.get(k)) is not int for k in ('MemTotal','MemAvailable','agent_pgrp_rss')):
            raise ValueError('the guest memory claim is incomplete')
        used=guest['MemTotal']-guest['MemAvailable']-guest['agent_pgrp_rss']
        if used>96<<20:misses.append({'metric':'guest_overhead_bytes','measured':used,'limit':96<<20})
    limits={'vmm':16<<20,'supervisor':24<<20,'egress':40<<20,'sealed_runtime':24<<20,'pagetables':(1<<30)//256,'residual':24<<20}
    cache=samples.get('disk_cache_delta_bytes',[])
    if not cache or any(type(v) is not int or v<0 for v in cache):raise ValueError('guest disk I/O has no native page-cache observation')
    for delta in cache:
        if delta>0:misses.append({'metric':'disk_page_cache_delta_bytes','measured':delta,'limit':0})
    for host in hosts:
        if host.get('mapping_error') or any('error' in p for p in host.get('processes',[])) or host.get('guest_ram_mapping_size')!=1<<30:
            raise ValueError('native process RSS or the complete original guest RAM mapping was not observed')
        if int(host['properties']['MemoryMax'])!=(1156<<20) or int(host['properties']['MemorySwapMax'])!=0:
            raise ValueError('the actual native unit did not retain its exact guest-plus-reserve/zero-swap cap')
        for part,limit in limits.items():
            value=host['parts'].get(part)
            if type(value) is not int or value<0:raise ValueError('native memory partition is unresolved: '+part)
            if value>limit:misses.append({'metric':part+'_bytes','measured':value,'limit':limit})
    return misses


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--revision',required=True)
    parser.add_argument('--issues',type=pathlib.Path,help='JSON map from substrate:metric to the actual opened issue URL')
    parser.add_argument('reports',nargs='+',type=pathlib.Path)
    args=parser.parse_args()
    if not re.fullmatch(r'[0-9a-f]{40}',args.revision):raise ValueError('use the exact source commit SHA')
    reports=[read(path,args.revision) for path in args.reports]
    arches={'x86_64':'amd64','amd64':'amd64','aarch64':'arm64','arm64':'arm64'}
    actual={(arches.get(d.get('host',{}).get('arch')),d.get('substrate')) for d in reports}
    required={(arch,substrate) for arch in ('amd64','arm64') for substrate in ('bare-metal','nested')}
    if actual!=required or len(reports)!=4:
        raise ValueError('record bare-metal and nested KVM separately for each shipped amd64/arm64 platform; absent hardware stays unproved')
    issues=json.loads(args.issues.read_text()) if args.issues else {}
    misses=[]
    for report in reports:
        for row in findings(report):
            key=arches[report['host']['arch']]+':'+report['substrate']+':'+row['metric']
            issue=issues.get(key,'')
            if not re.fullmatch(r'https://github\.com/[^/]+/[^/]+/issues/[1-9][0-9]*',issue):
                raise ValueError('open and record an issue for the measured target miss '+key)
            misses.append(dict(row,substrate=report['substrate'],issue=issue))
    print(json.dumps({'v':1,'revision':args.revision,'checked_reports':len(reports),'target_misses':misses,
                      'idle_stop_default_changed':False,'native_authority':False},indent=2))


if __name__=='__main__':
    try:main()
    except (OSError,KeyError,TypeError,ValueError) as error:
        print('conformance: '+str(error)+'. Try: run the real required suites and retain their complete original evidence.',file=sys.stderr)
        sys.exit(1)
