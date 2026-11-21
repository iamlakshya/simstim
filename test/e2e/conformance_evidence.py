"""Raw measurement records and strict statistical gates; no process runner."""
import hashlib, json, math, os, platform, re, tarfile, time


def percentile(values, fraction):
    if not values or any(type(v) not in (int, float) or not math.isfinite(v) or v < 0 for v in values):
        raise ValueError("measurement samples must be finite nonnegative numbers")
    return sorted(values)[max(0, math.ceil(len(values) * fraction) - 1)]


class Evidence:
    def __init__(self, destination, wall, substrate, revision):
        if not re.fullmatch(r"[0-9a-f]{40}", revision or ""):
            raise ValueError("E2E_REVISION must be the exact built source commit")
        if substrate not in ("bare-metal", "nested") or wall not in ("none", "process", "microvm"):
            raise ValueError("select the measured substrate and actual wall")
        self.path = os.path.abspath(destination)
        repository=os.path.realpath(os.path.join(os.path.dirname(__file__),'..','..'))
        if os.path.commonpath((repository,os.path.realpath(self.path)))==repository:
            raise ValueError('test-host change logs must be outside the repository')
        os.mkdir(self.path, 0o700)  # Never overwrite another run's evidence.
        self.data = {"v": 1, "revision": revision, "wall": wall, "substrate": substrate,
                     "client": {"os": platform.system(), "arch": platform.machine()},
                     "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                     "checks": {}, "samples": {}, "files": [], "complete": False}

    def check(self, name, passed, detail=None):
        self.data["checks"][name] = {"passed": bool(passed), "detail": detail}
        self.save()
        return bool(passed)

    def sample(self, name, value):
        self.data["samples"].setdefault(name, []).append(value)
        self.save()

    def action(self,name,**values):
        self.data.setdefault('host_actions',[]).append(dict(values,action=name,at_utc=time.strftime('%Y-%m-%dT%H:%M:%SZ',time.gmtime())))
        self.save()

    def file(self, name, data):
        if not re.fullmatch(r"[a-z0-9][a-z0-9._-]{0,100}", name) or len(data) > 32 << 20:
            raise ValueError("evidence file has an invalid name or exceeds 32 MiB")
        with open(os.path.join(self.path, name), "xb") as stream:
            stream.write(data)
        self.data["files"].append({"name": name, "size": len(data), "sha256": hashlib.sha256(data).hexdigest()})
        self.save()

    def save(self):
        temp = os.path.join(self.path, "report.json.new")
        with open(temp, "w", encoding="utf-8") as stream:
            json.dump(self.data, stream, indent=2, allow_nan=False)
            stream.write("\n")
        os.replace(temp, os.path.join(self.path, "report.json"))


def key_samples(records, clocks):
    """Upper bounds for bridge-to-PTY work, retaining clock uncertainty.

    Each guest timestamp occurred within an actual host RPC bracket. The
    intersection gives the guest-minus-host clock interval. No clock domain
    is silently treated as another kernel's CLOCK_MONOTONIC.
    """
    brackets = clocks.get("brackets", [])
    if not brackets:
        raise ValueError("no original guest/host clock brackets")
    lower, upper = -math.inf, math.inf
    for row in brackets:
        before, after, guest = (row[k] for k in ("host_before", "host_after", "guest_now"))
        if any(type(v) is not int or v <= 0 for v in (before, after, guest)) or after < before:
            raise ValueError("invalid clock bracket")
        lower, upper = max(lower, guest-after), min(upper, guest-before)
    if lower > upper:
        raise ValueError("clock brackets disagree; measure drift instead of inventing an offset")
    keys = {}
    for row in records:
        if row.get("ev") == "dropped":
            raise ValueError("the trace dropped records")
        if row.get("ev") != "key" or not row.get("bridge"):
            continue
        key = (row.get("trace"), row.get("key"))
        if key in keys or any(type(row.get(k)) is not int for k in ("t", "recv", "bridge")):
            raise ValueError("ambiguous or incomplete host key record")
        low = row["t"]-row["bridge"]-upper
        high = row["t"]-row["bridge"]-lower
        if high < 0:
            raise ValueError("key timing contradicts its native clock bracket")
        keys[key] = {"lower_ns": max(0, low), "upper_ns": high}
    if len(keys) < 1000:
        raise ValueError("the key-to-PTY gate needs at least 1000 joined keys")
    return list(keys.values()), upper-lower


def host_key_bounds(guest,client,bounds,acks):
    """Join 1000 controlled ACKs to raw original records in both processes.

    Only the supervisor's HOST clock determines the VM upper bound. Guest
    timestamps are used for their local ordering, never subtracted from the
    bridge clock. Output payloads are verified by the test fixture at the
    client; the supervisor observes frame arrivals only.
    """
    if type(bounds.get('v')) is not int or bounds.get('v')!=1 or not bounds.get('generation') or bounds.get('dropped') is not False:
        raise ValueError('the original supervisor trace is missing, changed or incomplete')
    if any(row.get('ev')=='dropped' for row in guest+client):
        raise ValueError('an original trace dropped records')
    echoes=acknowledged_refs(client,acks)
    guest_keys={(row.get('trace'),row.get('key')):row for row in guest if row.get('ev')=='key'}
    reads={(row.get('trace'),row.get('key')):row for row in guest if row.get('ev')=='read' and row.get('key')}
    original={}
    for row in bounds.get('samples',[]):
        key=(row.get('trace'),row.get('key'))
        if key in original:raise ValueError('the original host key window was duplicated')
        original[key]=row
    result=[]
    for key in echoes:
        row=original.get(key);write=guest_keys.get(key);read=reads.get(key)
        if row is None or write is None or read is None or read.get('t',-1)<write.get('t',0):
            raise ValueError('a required controlled key lacks its original guest read/write or host window')
        if any(type(row.get(k)) is not int for k in ('bridge','received','end','frames')) or not 0<row['bridge']<=row['received']<=row['end'] or row['frames']<1:
            raise ValueError('the actual host-clock key window is incomplete')
        result.append({'trace':key[0],'key':key[1],'upper_ns':row['end']-row['bridge'],'frames':row['frames']})
    return result


def acknowledged_refs(client,acks):
    if len(acks)!=1000 or any(row!={'index':n,'token':'key%04d'%n,'exact':True} for n,row in enumerate(acks)):
        raise ValueError('the fixture did not prove exactly 1000 single-inflight ACK windows')
    if any(row.get('ev')=='dropped' for row in client):raise ValueError('the actual client dropped trace records')
    client_keys={(row.get('trace'),row.get('key')) for row in client if row.get('ev')=='key'}
    echoes=[(row.get('trace'),row.get('key')) for row in client if row.get('ev')=='echo']
    if len(echoes)!=1000 or len(set(echoes))!=1000 or not set(echoes)<=client_keys:
        raise ValueError('the actual client did not retain 1000 distinct acknowledged keys')
    return echoes


def native_key_samples(records,client,acks):
    if any(row.get('ev')=='dropped' for row in records):raise ValueError('the original NONE writer dropped records')
    keys={}
    for row in records:
        if row.get('ev')!='key':continue
        key=(row.get('trace'),row.get('key'))
        if key in keys:raise ValueError('the original NONE key was duplicated')
        keys[key]=row
    values=[]
    for key in acknowledged_refs(client,acks):
        row=keys.get(key)
        if row is None or any(type(row.get(k)) is not int for k in ('t','recv','bridge')) or not 0<row['bridge']<=row['recv']<=row['t']:
            raise ValueError('the NONE baseline lacks its actual same-host bridge-to-PTY event')
        values.append(row['t']-row['bridge'])
    return values


def read_trace_bundle(path,label):
    found={};members=total=0
    with tarfile.open(path,'r:gz') as archive:
        for item in archive:
            members+=1;total+=item.size
            if members>512 or total>64<<20:raise ValueError('the original bundle exceeds the bounded diagnostic envelope')
            suffix=next((name for name in ('guest-trace.jsonl','guest-clock.json','host-upper.json') if item.name.endswith('/'+name)),None)
            if suffix is None:continue
            if not item.isfile() or item.size>16<<20 or suffix in found:raise ValueError('the original trace member is duplicated or unbounded')
            stream=archive.extractfile(item)
            if stream is None:raise ValueError('the original trace member is unreadable')
            data=stream.read(16<<20|1)
            if len(data)>16<<20:raise ValueError('the original trace member exceeds its bound')
            found[suffix]=data
    if 'guest-trace.jsonl' not in found or label=='microvm' and not {'guest-clock.json','host-upper.json'}<=found.keys():
        raise ValueError('the bundle lacks its original holder writer or native supervisor bounds')
    records=[json.loads(line) for line in found['guest-trace.jsonl'].splitlines() if line]
    return records,json.loads(found['guest-clock.json']) if 'guest-clock.json' in found else None,json.loads(found['host-upper.json']) if 'host-upper.json' in found else None


def throughput_samples(text,kind):
    wanted={'BenchmarkRelayThroughput'} if kind=='supervisor' else {'BenchmarkThroughput/window=%dKiB'%window for window in (64,128,256,512,1024)}
    samples={name:[] for name in wanted}
    for line in text.splitlines():
        fields=line.split()
        if not fields:continue
        name=re.sub(r'-[0-9]+$','',fields[0])
        if name not in wanted:continue
        if 'MB/s' not in fields:raise ValueError('an actual throughput result has no measured byte rate')
        index=fields.index('MB/s')
        value=float(fields[index-1])*1000000/(1<<20)
        if not math.isfinite(value) or value<=0:raise ValueError('invalid actual throughput result')
        if len(fields)<2 or int(fields[1])!=1024:raise ValueError('the socket-pair benchmark did not pass its specified 1 GiB')
        samples[name].append(value)
    if any(len(values)!=3 for values in samples.values()):raise ValueError('every required throughput case needs three complete actual 1 GiB runs')
    return samples
