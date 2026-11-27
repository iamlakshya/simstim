"""Offline tests of a run that a signal ends: python3 test/e2e/test_signals.py.
A child run in run.py's frame runs the harness's own shell commands on a
local folder that stands for the host's home. SIGTERM and SIGHUP, also
while it takes its lock, still run its clean-up; after SIGKILL, the
clean-up by its token removes what it left. The owner's files stay.
"""
import os, shutil, signal, subprocess, sys, tempfile, unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from fakes import HERE, OWNER, Ctx, Host, fake_client, local_shell, read, write  # noqa: E402
import owned as ow  # noqa: E402
import run_parts as rp  # noqa: E402


# A child run in run.py's frame: it turns the signals into an exit, and in
# the try it takes the lock, makes its folder, and a session comes late; then it waits, until the test sends it
# a signal. Its finally runs finish. With "acquire", the signal comes while
# it takes the lock: the host made the lock, and the answer is not back.
CHILD = r'''
import json, os, subprocess, sys, time, types
sys.path.insert(0, sys.argv[1])
import owned, run_parts
home, token, mode = sys.argv[2], sys.argv[3], sys.argv[4]
owned.RUN = token
owned.NONCE = "n" * 32
run_parts.LIST_WAIT = 0
env = {"HOME": home, "PATH": os.environ.get("PATH", "/usr/bin:/bin")}
sessions = {}
def sh(cmd, data=None):
    return subprocess.run(["sh", "-c", cmd], env=env, input=data,
                          stdin=None if data is not None else subprocess.DEVNULL,
                          capture_output=True, timeout=15)
def host_rc(cmd, data=None):
    if cmd.startswith("~/helper rm "):
        return 0 if sessions.pop(cmd.split()[2], None) else 1
    rc = sh(cmd, data).returncode
    if mode == "acquire" and cmd.startswith("mkdir ~/.e2e-lock"):
        print("READY", flush=True)
        time.sleep(60)
    return rc
def host_out(cmd):
    r = sh(cmd)
    return r.stdout.decode() if r.returncode == 0 else ""
def run_cmd(argv, env=None, timeout=60, cwd=None):
    if argv[1] == "ls":
        return 0, json.dumps([{"v": 1, "host": "box", "sessions": [{"id": i, "name": n} for i, n in sessions.items()]}]), ""
    raise AssertionError(argv)
owned.run_cmd = run_cmd
c = types.SimpleNamespace(cli="client", HOST="box", NAME="client", HELPER="~/helper", envx={}, host_rc=host_rc, host_out=host_out,
                          ok=lambda cond, msg: cond)
created = []
state = {"before": None}
run_parts.exit_on_signals()
try:
    locked, holder = owned.take_lock(c)
    if not locked:
        sys.exit("refused: " + holder)
    owned.make_run_dir(c)
    state["before"] = set()
    sessions["late1234"] = "e2e-x-" + token
    sh("echo data > ~/.e2e-run-%s/seq.bin" % token)
    print("READY", flush=True)
    time.sleep(60)
finally:
    clean = False
    for _ in (1, 2, 3, 4):
        try:
            run_parts.ignore_signals()
            clean = run_parts.finish(c, created, state)
            if "finished" in state:
                break
        except BaseException:
            pass
    print("FINISH", clean, flush=True)
'''


class Signals(unittest.TestCase):
    """A run that SIGTERM or SIGHUP ends still runs its clean-up, also when
    the signal comes while it takes the lock: it leaves nothing on the
    host, and the owner's files stay. A run that SIGKILL ends runs none,
    and what it leaves holds its token, so the clean-up by that token
    removes it all. A lock of another run is never touched."""
    def setUp(self):
        self.home = tempfile.mkdtemp(prefix="e2e-home-")
        self.addCleanup(shutil.rmtree, self.home, True)
        for rel, text in OWNER.items():
            write(os.path.join(self.home, rel), text)
        self.child = os.path.join(self.home, "child.py")
        write(self.child, CHILD)
        self.addCleanup(setattr, ow, "RUN", ow.RUN)
        self.addCleanup(setattr, ow, "NONCE", ow.NONCE)

    def run_child(self, sig, mode="run", burst=False):
        p = subprocess.Popen([sys.executable, "-I", self.child, HERE, self.home, "cccccc", mode],
                             stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        try:
            if sig is not None:
                self.assertEqual(p.stdout.readline().strip(), "READY")
                p.send_signal(sig)
                if burst:
                    for extra in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
                        try:
                            p.send_signal(extra)
                        except ProcessLookupError:
                            pass
            out, _ = p.communicate(timeout=60)
        finally:
            if p.poll() is None:
                p.kill()
                p.wait()
        return p.returncode, out

    def left(self):
        return {rel: os.path.exists(os.path.join(self.home, rel)) for rel in [".e2e-lock", ".e2e-run-cccccc"]}

    def assertOwnerUntouched(self):
        for rel, text in OWNER.items():
            self.assertEqual(read(os.path.join(self.home, rel)), text, rel)

    def test_sigterm_and_sighup(self):
        for sig in [signal.SIGTERM, signal.SIGHUP]:
            with self.subTest(sig=sig.name):
                code, out = self.run_child(sig)
                self.assertEqual(code, 128 + sig)
                self.assertIn("FINISH True", out)
                self.assertIn("late1234", out)  # the late session, deleted by ID
                self.assertEqual(self.left(), {".e2e-lock": False, ".e2e-run-cccccc": False})
                self.assertOwnerUntouched()

    def test_a_signal_while_the_run_takes_the_lock(self):
        for sig in [signal.SIGTERM, signal.SIGHUP]:
            with self.subTest(sig=sig.name):
                code, out = self.run_child(sig, mode="acquire")
                self.assertEqual(code, 128 + sig)
                self.assertIn("FINISH True", out)
                self.assertFalse(os.path.exists(os.path.join(self.home, ".e2e-lock")))
                self.assertOwnerUntouched()

    def test_a_lock_of_another_run(self):
        write(os.path.join(self.home, ".e2e-lock", "run"), "dddddd")
        code, out = self.run_child(None)
        self.assertEqual(code, 1)
        self.assertIn("FINISH True", out)
        self.assertEqual(read(os.path.join(self.home, ".e2e-lock", "run")), "dddddd")
        self.assertEqual(self.left(), {".e2e-lock": True, ".e2e-run-cccccc": False})

    def test_sigkill_then_the_clean_up_by_its_token(self):
        code, out = self.run_child(signal.SIGKILL)
        self.assertEqual(code, -signal.SIGKILL)
        self.assertEqual(self.left(), {".e2e-lock": True, ".e2e-run-cccccc": True})
        c = Ctx(Host())
        c.host_rc, c.host_out = local_shell(self.home)
        # Another run finds the lock, and its message names the manual recovery commands.
        ow.RUN = "dddddd"
        self.assertEqual(ow.take_lock(c), (False, "cccccc"))
        help_text = ow.leftover_help(c, "cccccc")
        self.assertIn("~/.e2e-run-cccccc", help_text)
        # Another nonce leaves the killed run's folder untouched (item O).
        ow.RUN, ow.NONCE = "cccccc", "other-nonce"
        self.assertTrue(ow.remove_run_dir(c))
        self.assertTrue(os.path.isdir(os.path.join(self.home, ".e2e-run-cccccc")))
        # The manual recovery commands printed by leftover_help remove the folder and lock.
        self.assertEqual(c.host_rc('[ "$(cat ~/.e2e-run-cccccc/.e2e-run)" = cccccc ] && rm -rf ~/.e2e-run-cccccc'), 0)
        self.assertEqual(c.host_rc(ow.UNLOCK), 0)
        self.assertEqual(self.left(), {".e2e-lock": False, ".e2e-run-cccccc": False})
        self.assertOwnerUntouched()

    def test_repeated_signals_do_not_abort_finish(self):
        code, out = self.run_child(signal.SIGTERM, burst=True)
        self.assertIn(code, (128 + signal.SIGTERM, 128 + signal.SIGHUP, 1, -signal.SIGINT))
        self.assertIn("FINISH True", out)
        self.assertEqual(self.left(), {".e2e-lock": False, ".e2e-run-cccccc": False})
        self.assertOwnerUntouched()


if __name__ == "__main__":
    unittest.main()
