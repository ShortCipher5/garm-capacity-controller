import importlib.util, pathlib, subprocess, sys, unittest
spec=importlib.util.spec_from_file_location('preflight',pathlib.Path(__file__).with_name('preflight.py'))
module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
class OutputTests(unittest.TestCase):
    def child(self,code):
        return subprocess.Popen([sys.executable,'-c',code],stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    def cleanup(self,p):
        if p.poll() is None:p.kill()
        p.wait();p.stdout.close();p.stderr.close()
    def test_bounded_report(self):
        p=self.child("print('report')")
        try:self.assertEqual(module.bounded_output(p),b'report\n')
        finally:self.cleanup(p)
    def test_each_output_stream_bounded(self):
        for stream in ('stdout','stderr'):
            p=self.child("import sys;sys.%s.write('x'*70000)"%stream)
            try:
                with self.assertRaises(RuntimeError):module.bounded_output(p)
            finally:self.cleanup(p)
    def test_failure_not_report(self):
        p=self.child("raise SystemExit(1)")
        try:
            with self.assertRaises(RuntimeError):module.bounded_output(p)
        finally:self.cleanup(p)
if __name__=='__main__':unittest.main()
