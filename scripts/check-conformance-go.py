#!/usr/bin/env python3
"""Reject failed, incomplete or skipped native assurance in go test -json.

This checks a real run log. It does not run Go, provide a hardware substitute,
or change any test count, race, latency, memory or throughput threshold.
"""
import argparse,json,pathlib,sys


def inspect(lines,required):
    ended={};skipped=[];failed=[];events=0
    for line in lines:
        row=json.loads(line);events+=1
        action=row.get('Action');package=row.get('Package');test=row.get('Test')
        if action=='skip':skipped.append({'package':package,'test':test})
        if action=='fail':failed.append({'package':package,'test':test})
        if package and not test and action in ('pass','fail','skip'):ended[package]=action
    if not events or not required<=ended.keys() or any(ended[p]!='pass' for p in required):
        raise ValueError('the complete required native package run did not finish successfully')
    if failed or skipped:
        raise ValueError('native assurance remains unproved: '+json.dumps({'failed':failed,'skipped':skipped},sort_keys=True))
    return {'v':1,'packages':ended,'skipped':skipped,'native_authority':False}


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('log',type=pathlib.Path)
    parser.add_argument('--package',action='append',required=True)
    args=parser.parse_args()
    with args.log.open() as stream:result=inspect(stream,set(args.package))
    print(json.dumps(result,indent=2))


if __name__=='__main__':
    try:main()
    except (OSError,ValueError) as error:
        print('conformance: '+str(error)+'. Try: run every required native package on its prepared supported host and retain all results.',file=sys.stderr)
        sys.exit(1)
