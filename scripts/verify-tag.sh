#!/usr/bin/env bash
# The release job's signed-tag gate (decision 0041). The release job runs the
# copy of this script that main has, never the tagged commit's copy, so a tag
# cannot bring its own gate. It runs in the job's checkout, before any build
# step, and fails unless the tag is an annotated tag that a key in
# .github/tag-signers signed, on a commit that is on main and that the job
# checked out. The keys come from main too, so a tag cannot bring its own key.
#
#   verify-tag.sh <tag>
#
# It needs git 2.34 or later and OpenSSH 8.8 or later, and no private key: a
# public key checks a signature. scripts/test-verify-tag.sh tests it in a
# temporary repository. The numbers in the comments are the steps of section
# 20.7 of docs/contributing/isolation-spec.md. Step 1, the checkout, is in
# release.yml.
set -euo pipefail

fail() { # fail <message> <hint>
    echo "release: $1" >&2
    echo "Try: $2" >&2
    exit 1
}

[ $# -eq 1 ] && [ -n "$1" ] ||
    fail "verify-tag.sh needs one argument, the tag to check." "pass the tag, as in verify-tag.sh v0.6.0."
tag=$1

# The kinds of key that may sign a release tag: hardware keys only.
# scripts/test-verify-tag.sh adds plain ed25519 keys to its own copy of this
# line, because it cannot make a hardware key.
key_types='sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com'

# quote <value>: the value as one shell word, for a command in a hint that a
# person may paste into a shell. A tag name may hold ; $ ` ' | and more, so a
# value with any other character than these safe ones goes in single quotes.
quote() {
    case $1 in
    '' | *[!A-Za-z0-9._/+=@%,-]*) printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")" ;;
    *) printf '%s' "$1" ;;
    esac
}

# A tag name that starts with "-": git tag cannot make one, so no command can
# tag it again. The hint deletes it by its full ref instead, and the
# maintainer chooses a release tag name. (release.yml only runs for v*.)
case $tag in
-*)
    ref=$(quote "refs/tags/$tag")
    fail "$tag starts with \"-\", and git tag cannot make a tag with that name." \
        "delete it by its full ref, with git push --delete origin $ref and git update-ref -d $ref, then choose a release tag name that starts with v."
    ;;
esac

# at_least <have> <major> <minor>: true when the version <have>, written as
# "major.minor", is <major>.<minor> or later.
at_least() {
    local maj=${1%%.*} min=${1#*.}
    [ "$maj" -gt "$2" ] || { [ "$maj" -eq "$2" ] && [ "$min" -ge "$3" ]; }
}

# 2. Git 2.34 added SSH signatures, and its notes warn against the
# ssh-keygen of OpenSSH 8.7.
git_version=$(git --version | sed -n 's/^git version \([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p' || true)
if [ -z "$git_version" ] || ! at_least "$git_version" 2 34; then
    fail "the runner has git ${git_version:-of an unknown version}, and the check needs 2.34 or later." \
        "pin a newer runner image in release.yml."
fi
ssh_version=$(ssh -V 2>&1 | sed -n 's/^OpenSSH_[A-Za-z_]*\([0-9][0-9]*\.[0-9][0-9]*\).*/\1/p' || true)
if [ -z "$ssh_version" ] || ! at_least "$ssh_version" 8 8; then
    fail "the runner has OpenSSH ${ssh_version:-of an unknown version}, and the check needs 8.8 or later." \
        "pin a newer runner image in release.yml."
fi

# 3. Fetch main and the tag again. If the tag moved after the checkout, step 7
# sees it.
git fetch --quiet --force origin \
    "+refs/heads/main:refs/remotes/origin/main" "+refs/tags/$tag:refs/tags/$tag" ||
    fail "cannot fetch main and $tag from origin. The gate fetches with no token, so it needs a public repository." \
        "run the release job again. If it fails again, check that the tag $tag still exists. A private fork must give the gate's fetch a read-only token, and remove it before the build."

# The commands that tag again the commit that the tag names now, for the
# hints. Every value in them is quoted. "--" and "refs/tags/" keep a tag that
# starts with "-" from reading as an option.
seen=$(git rev-parse --verify -q "refs/tags/$tag^{commit}" || true)
qtag=$(quote "$tag")
qseen=${seen:+$(quote "$seen")}
retag="git tag -s -f -m $qtag -- $qtag ${qseen:-<the release commit>}, then git push --force origin $(quote "refs/tags/$tag")"

# 4. A lightweight tag has no signature to check.
if [ "$(git cat-file -t "refs/tags/$tag")" != tag ]; then
    fail "$tag is a lightweight tag, and a release needs a signed annotated tag." "$retag."
fi

# 5. The keys come from main, never from the tag. Each line that is not a
# comment must be a hardware key for git signatures, and nothing more.
dir=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/verify-tag.XXXXXX") ||
    fail "cannot make a temporary folder in ${RUNNER_TEMP:-${TMPDIR:-/tmp}}." "run the release job again."
trap 'rm -rf "$dir"' EXIT
if ! git show "refs/remotes/origin/main:.github/tag-signers" >"$dir/signers" 2>/dev/null; then
    fail "main has no .github/tag-signers, so no key may sign a release." \
        "add the file to main through a pull request, with the line of your tag key."
fi
bad=$(grep -n -v -E -e '^[[:space:]]*(#|$)' \
    -e "^[^[:space:]]+ namespaces=\"git\" ($key_types) [A-Za-z0-9+/]+=*( [^[:space:]].*)?\$" \
    "$dir/signers" | cut -d: -f1 | head -n 1 || true)
if [ -n "$bad" ]; then
    fail "line $bad of .github/tag-signers on main is not a hardware key with namespaces=\"git\"." \
        "fix the line through a pull request. A key line reads: <email> namespaces=\"git\" sk-ssh-ed25519@openssh.com AAAA... <comment>."
fi

# 6. A good SSH signature by a key in that file. Git checks the last
# signature block of the tag. Only an SSH signature counts: git would check
# any other kind against the runner's own keys, which the project does not
# choose. Pin gpg.ssh.program so that no git config can replace it.
kind=$(git cat-file tag "refs/tags/$tag" | grep '^-----BEGIN ' | tail -n 1 || true)
if [ "$kind" != "-----BEGIN SSH SIGNATURE-----" ] ||
    ! git -c gpg.ssh.program=ssh-keygen -c gpg.ssh.allowedSignersFile="$dir/signers" \
        verify-tag "refs/tags/$tag"; then
    fail "$tag is not signed by a key in .github/tag-signers on main." \
        "sign it with your key from that file: $retag. If your key is new, add it to the file through a pull request first."
fi

# 6, continued. A signed tag of an older release, pushed again under this
# name, names an older commit on main, which step 7 would accept. The tag
# object keeps the name that it was signed with.
name=$(git cat-file tag "refs/tags/$tag" | sed -n '1,/^$/s/^tag //p')
if [ "$name" != "$tag" ]; then
    fail "$tag is a signed tag made for $name, under another name." \
        "if you did not push it, delete it and find out who did. Otherwise, to tag the commit that it names now: $retag."
fi

# 7. The tag names a commit. The job builds the commit that it checked, and
# that commit is on main.
commit=$(git rev-parse --verify -q "refs/tags/$tag^{commit}") ||
    fail "$tag does not name a commit." "tag a commit that is on main."
if [ "$commit" != "$(git rev-parse HEAD)" ]; then
    fail "$tag now names another commit than the one this job checked out." \
        "the push that moved $tag starts its own release run, which checks the tag again. Use that run."
fi
if ! git merge-base --is-ancestor "$commit" refs/remotes/origin/main; then
    fail "the commit of $tag is not on main." "tag a commit that is on main."
fi
echo "release: $tag is signed by a key in .github/tag-signers on main, and names this job's commit, which is on main."
