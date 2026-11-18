import importlib.util, pathlib, subprocess, sys, unittest
spec=importlib.util.spec_from_file_location('upload',pathlib.Path(__file__).with_name('upload.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
class OutputTests(unittest.TestCase):
    def runchild(self,source):
        child=subprocess.Popen([sys.executable,'-c',source],stdout=subprocess.PIPE)
        try:return m.bounded_output(child)
        finally:
            if child.poll() is None:child.kill()
            child.wait();child.stdout.close()
    def test_success(self):self.assertEqual(self.runchild("print('receipt')"),b'receipt\n')
    def test_overflow(self):
        with self.assertRaises(ValueError):self.runchild("print('x'*20000)")
    def test_failure(self):
        with self.assertRaises(ValueError):self.runchild("raise SystemExit(1)")
if __name__=='__main__':unittest.main()
