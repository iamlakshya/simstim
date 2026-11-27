"""Route joined CLI checks through the canonical marker-verified context."""
import run_parts


def microvm_checks(c,measurements=False):
    """Run the real selected VM suite only under the canonical host marker."""
    if c.DISPOSABLE:
        import microvm
        microvm.run(c,measurements=measurements)
    else:
        c.ok(False,"microvm conformance needs an authenticated disposable host; no hardware check ran")


def finish_conformance(c,clean,created,state):
    """Publish completion only after the existing runner's own final cleanup."""
    evidence=getattr(c,"conformance_evidence",None)
    if evidence is not None:
        evidence.check("runner_cleanup",clean,{"remaining_owned_ids":list(created),"finished":"finished" in state})
        evidence.data["complete"]=bool(evidence.data.get("suite_finished") and clean and all(row["passed"] for row in evidence.data["checks"].values()))
        evidence.save()


def sandbox_host_checks(c, selected=False):
    """Run sandbox-hosts, or report the owner-host coverage policy."""
    if c.DISPOSABLE:
        import sandbox_hosts
        sandbox_hosts.run(c)
    elif selected:
        c.ok(False, "sandbox-hosts needs an authenticated disposable host; no check ran")
    else:
        run_parts.skip("sandbox hosts")


def socket_checks(c, selected=False):
    """Run session-sockets, or report the owner-host coverage policy."""
    if c.DISPOSABLE:
        import session_sockets
        session_sockets.run(c)
    elif selected:
        c.ok(False, "session-sockets needs an authenticated disposable host; no check ran")
    else:
        run_parts.skip("session sockets")


def host_git_checks(c, selected=False):
    """Run host-git, or report the owner-host coverage policy."""
    if c.DISPOSABLE:
        import host_git
        host_git.run(c)
    elif selected:
        c.ok(False, "host-git needs an authenticated disposable host; no check ran")
    else:
        run_parts.skip("host git")


def verbs_checks(c, selected=False):
    """Run session-verbs, or report the owner-host coverage policy."""
    if c.DISPOSABLE:
        import session_verbs
        session_verbs.run(c)
    elif selected:
        c.ok(False, "session-verbs needs an authenticated disposable host; no check ran")
    else:
        run_parts.skip("session verbs")


def host_checks(c, selected=False):
    """Run host-commands, or report the owner-host coverage policy."""
    if c.DISPOSABLE:
        import host_commands
        host_commands.run(c)
    elif selected:
        c.ok(False, "host-commands needs an authenticated disposable host; no check ran")
    else:
        run_parts.skip("host commands")
