"""Offline tests of native log completeness; no native suite is executed."""
import importlib.util,json,pathlib,unittest

spec=importlib.util.spec_from_file_location('conformance_go',pathlib.Path(__file__).with_name('check-conformance-go.py'))
gate=importlib.util.module_from_spec(spec);spec.loader.exec_module(gate)


class NativeLog(unittest.TestCase):
    def log(self,*rows):return [json.dumps(row) for row in rows]

    def test_a_started_test_is_not_a_completed_package(self):
        for rows in ([],self.log({'Action':'start','Package':'p'}),self.log({'Action':'pass','Package':'q'})):
            with self.assertRaises(ValueError):gate.inspect(rows,{'p'})

    def test_failure_or_skip_cannot_issue_a_passing_summary(self):
        for action in ('skip','fail'):
            rows=self.log({'Action':action,'Package':'p','Test':'TestOriginalNative'}, {'Action':'pass','Package':'p'})
            with self.assertRaises(ValueError):gate.inspect(rows,{'p'})

    def test_only_complete_real_log_shapes_are_accepted(self):
        rows=self.log({'Action':'run','Package':'p','Test':'TestOriginalNative'},
                      {'Action':'pass','Package':'p','Test':'TestOriginalNative'}, {'Action':'pass','Package':'p'})
        self.assertEqual(gate.inspect(rows,{'p'})['packages'],{'p':'pass'})


if __name__=='__main__':unittest.main()
