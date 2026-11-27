"""Offline statistical/parser tests; these assert no VM or native result."""
import json, os, pathlib, sys, unittest

sys.path.insert(0,os.path.dirname(__file__))
import conformance_evidence as evidence


class Samples(unittest.TestCase):
    def test_nearest_rank_preserves_p99_outliers(self):
        values=[1]*98+[10,100]
        self.assertEqual(evidence.percentile(values,.99),10)
        self.assertEqual(evidence.percentile(values,1),100)
        for bad in ([],[float('nan')],[float('inf')],[-1],[True]):
            with self.assertRaises(ValueError):evidence.percentile(bad,.5)

    def test_cross_kernel_times_need_original_clock_brackets(self):
        rows=[{'ev':'key','trace':'fixture','key':n,'t':1500+n,'recv':1400+n,'bridge':1000+n} for n in range(1000)]
        clock={'brackets':[{'host_before':2000,'host_after':2020,'guest_now':2500}]}
        values,width=evidence.key_samples(rows,clock)
        self.assertEqual(width,20)
        self.assertTrue(all(v=={'lower_ns':0,'upper_ns':20} for v in values))
        with self.assertRaises(ValueError):evidence.key_samples(rows,{})
        with self.assertRaises(ValueError):evidence.key_samples(rows[:-1],clock)
        with self.assertRaises(ValueError):evidence.key_samples(rows+[rows[0]],clock)
        with self.assertRaises(ValueError):evidence.key_samples(rows+[{'ev':'dropped'}],clock)

    def test_clock_conflicts_are_not_reported_as_low_latency(self):
        clock={'brackets':[{'host_before':10,'host_after':20,'guest_now':100},
                           {'host_before':10,'host_after':20,'guest_now':200}]}
        with self.assertRaises(ValueError):evidence.key_samples([],clock)

    def controlled(self):
        guest=[];client=[];rows=[];acks=[]
        for index in range(1000):
            key=index+1
            guest.extend([{'ev':'key','trace':'controlled','key':key,'t':10,'recv':5,'bridge':1000},
                          {'ev':'read','trace':'controlled','key':key,'t':11}])
            client.extend([{'ev':'key','trace':'controlled','key':key},{'ev':'echo','trace':'controlled','key':key}])
            rows.append({'trace':'controlled','key':key,'bridge':1000,'received':1100,'end':1200,'frames':8})
            acks.append({'index':index,'token':'key%04d'%index,'exact':True})
        return guest,client,{'v':1,'generation':'diagnostic-unit-fixture','dropped':False,'samples':rows},acks

    def test_host_clock_bound_does_not_subtract_the_guest_clock(self):
        guest,client,bounds,acks=self.controlled()
        result=evidence.host_key_bounds(guest,client,bounds,acks)
        self.assertEqual(len(result),1000)
        self.assertTrue(all(row['upper_ns']==200 and row['frames']==8 for row in result))

    def test_partial_unrelated_or_changed_native_windows_are_inconclusive(self):
        for field,value in (('generation',''),('dropped',True)):
            guest,client,bounds,acks=self.controlled();bounds[field]=value
            with self.assertRaises(ValueError):evidence.host_key_bounds(guest,client,bounds,acks)
        guest,client,bounds,acks=self.controlled();acks[9]['exact']=False
        with self.assertRaises(ValueError):evidence.host_key_bounds(guest,client,bounds,acks)
        guest,client,bounds,acks=self.controlled();bounds['samples'][3]['frames']=0
        with self.assertRaises(ValueError):evidence.host_key_bounds(guest,client,bounds,acks)
        guest,client,bounds,acks=self.controlled();client.pop()
        with self.assertRaises(ValueError):evidence.host_key_bounds(guest,client,bounds,acks)


if __name__=='__main__':unittest.main()
