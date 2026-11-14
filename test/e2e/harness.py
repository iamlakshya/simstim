"""Drive the client in a real pseudo-terminal, like a person in Ghostty would."""
import os, pty, select, struct, fcntl, termios, time, sys, re, signal

class Term:
    def __init__(self, argv, cols=120, rows=36, env=None, inherit=True):
        # inherit=False: the program gets env alone, none of this process's
        # environment (boundary.term, for every client of the e2e run).
        self.pid, self.fd = pty.fork()
        if self.pid == 0:
            e = dict(os.environ) if inherit else {}
            e.update(env or {})
            os.execvpe(argv[0], argv, e)
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        self.buf = b""
    def read(self, timeout=0.5):
        end = time.time() + timeout
        while time.time() < end:
            r, _, _ = select.select([self.fd], [], [], max(0, end - time.time()))
            if not r: break
            try: data = os.read(self.fd, 65536)
            except OSError: break
            if not data: break
            self.buf += data
        return self.buf
    def wait_for(self, pattern, timeout=20):
        end = time.time() + timeout
        while time.time() < end:
            self.read(0.2)
            if re.search(pattern, self.buf): return True
        return False
    def send(self, b):
        os.write(self.fd, b)
    def resize(self, cols, rows):
        # The kernel sends SIGWINCH to the client, as a terminal would.
        fcntl.ioctl(self.fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
    def exit_code(self, timeout=10):
        if getattr(self, "code", None) is not None: return self.code
        end = time.time() + timeout
        while True:  # looks at least once, also with a timeout of 0
            pid, st = os.waitpid(self.pid, os.WNOHANG)
            if pid:
                self.code = os.waitstatus_to_exitcode(st)
                return self.code
            if time.time() >= end:
                return None
            self.read(0.1)
    def close(self):
        """Kills the client if it still runs, reaps it, and closes the terminal."""
        if getattr(self, "code", None) is None:
            try:
                pid, st = os.waitpid(self.pid, os.WNOHANG)
                if not pid:
                    os.kill(self.pid, signal.SIGKILL)
                    pid, st = os.waitpid(self.pid, 0)
                self.code = os.waitstatus_to_exitcode(st)
            except (ChildProcessError, ProcessLookupError):
                pass
        try: os.close(self.fd)
        except OSError: pass
