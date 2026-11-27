"""Automatic helper installation stays disabled on every owner-host path."""
import os, shutil, sys, tempfile, types, unittest
from unittest import mock

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import allowed  # noqa: E402
import boundary  # noqa: E402
from fakes import write  # noqa: E402


class NoInstall(unittest.TestCase):
    def setUp(self):
        base = tempfile.mkdtemp(prefix="e2e-no-install-")
        self.addCleanup(shutil.rmtree, base, True)
        hosts = os.path.join(base, "hosts.toml")
        write(hosts, '[[host]]\nname = "box"\nssh = "dev@box"\n')
        self.root = boundary.Root("client", cli="client", hosts=hosts, host="box", base=base)
        self.addCleanup(self.root.remove)
        self.addCleanup(setattr, boundary, "CTX", boundary.CTX)
        self.c = types.SimpleNamespace(cli="client", HOST="box", NAME="client", DISPOSABLE=False, root=self.root)
        boundary.CTX = self.c

    def test_root_forces_guard_after_caller_overrides(self):
        for extra in ({}, {"CLIENT_NO_INSTALL": "0"}, {"CLIENT_NO_INSTALL": ""}):
            with self.subTest(extra=extra):
                self.assertEqual(self.root.env(extra)["CLIENT_NO_INSTALL"], "1")
        self.c.DISPOSABLE = True
        self.assertEqual(self.root.env({"CLIENT_NO_INSTALL": "0"})["CLIENT_NO_INSTALL"], "0")

    def test_every_process_boundary_refuses_missing_or_changed_guard(self):
        for value in (None, "", "0", "true"):
            env = self.root.env()
            if value is None:
                env.pop("CLIENT_NO_INSTALL")
            else:
                env["CLIENT_NO_INSTALL"] = value
            with mock.patch.object(boundary._subprocess, "run") as run, \
                    mock.patch.object(boundary._harness, "Term") as term:
                with self.assertRaisesRegex(boundary.Refused, "automatic helper installation"):
                    boundary.run(["client", "version", "--json"], timeout=1, env=env)
                with self.assertRaisesRegex(boundary.Refused, "automatic helper installation"):
                    boundary.Term(["client", "version", "--json"], env=env)
                run.assert_not_called()
                term.assert_not_called()

    def test_identity_commands_require_id_and_end_of_options(self):
        boundary.OWNED.add("mine1234")
        self.addCleanup(boundary.OWNED.clear)
        for verb in ("attach", "pull"):
            good = [verb, "--id", "--", "box/mine1234"]
            self.assertTrue(allowed.client_allowed(good, self.c))
            self.assertFalse(allowed.client_allowed([verb, "box/mine1234"], self.c))
            self.assertFalse(allowed.client_allowed([verb, "--", "box/mine1234"], self.c))
            self.assertFalse(allowed.client_allowed([verb, "--id", "box/mine1234"], self.c))
            self.assertFalse(allowed.client_allowed([verb, "--id", "--", "box/mine1234", "--force"], self.c))
