#!/usr/bin/env bash
# Tests the release job's signed-tag gate (scripts/verify-tag.sh, and its
# step in .github/workflows/release.yml) in a temporary folder with
# throwaway keys. It never pushes a tag: "origin" is a local repository where
# the test tags as a maintainer would. Each run of the gate is in a fresh
# checkout made as actions/checkout makes it: fetched from origin, detached at
# the tag, with no local main. Most cases run the gate's step from
# release.yml, which runs main's copy of the gate. Needs git 2.34 or later
# and OpenSSH 8.8 or later.
set -euo pipefail
cd "$(dirname "$0")/.."
root=$PWD
gate="$root/scripts/verify-tag.sh"
workflow="$root/.github/workflows/release.yml"

# On an older machine, skip, except in CI, whose runner has both tools.
too_old=$(
    git --version | sed -n 's/^git version \([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p' |
        awk '{ if ($1 < 2 || ($1 == 2 && $2 < 34)) print "git" }'
    ssh -V 2>&1 | sed -n 's/^OpenSSH_[A-Za-z_]*\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2/p' |
        awk '{ if ($1 < 8 || ($1 == 8 && $2 < 8)) print "OpenSSH" }'
)
if [ -n "$too_old" ]; then
    [ -z "${CI:-}" ] || { echo "test-verify-tag: FAIL, this runner's $too_old is too old for the gate" >&2; exit 1; }
    echo "test-verify-tag: skipped, the gate needs git 2.34 and OpenSSH 8.8, and this machine's $too_old is older"
    exit 0
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
die() { # die <message>
    echo "test-verify-tag: FAIL, $1" >&2
    [ ! -f "$work/out" ] || cat "$work/out" >&2
    exit 1
}

# Keep the user's own home, git config, signing settings and GPG keys out of
# the test.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE SSH_AUTH_SOCK SSH_AGENT_PID
export HOME="$work/home" GNUPGHOME="$work/home/.gnupg"
mkdir -p "$GNUPGHOME"
chmod 700 "$GNUPGHOME"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=Maintainer GIT_AUTHOR_EMAIL=maintainer@example.com
export GIT_COMMITTER_NAME=Maintainer GIT_COMMITTER_EMAIL=maintainer@example.com
export RUNNER_TEMP="$work/runner"
mkdir "$RUNNER_TEMP"

# --- release.yml: what the release job runs, read from the file itself.

# step <pattern>: the step of release.yml whose text matches <pattern>.
step() {
    awk -v pat="$1" '
        function flush() { if (blk ~ pat) printf "%s", blk; blk = "" }
        { match($0, /^ */); ind = RLENGTH }
        ind == 6 && /^      - / { flush(); in_step = 1 }
        $0 != "" && ind < 6 { flush(); in_step = 0 }
        in_step { blk = blk $0 "\n" }
        END { flush() }
    ' "$workflow"
}
# The gate's step comes right after the checkout, before every build step.
first=$(awk '/^  release:/ { on = 1; next } /^  [a-z]/ { on = 0 } on && /^      - / { print; if (++n == 2) exit }' "$workflow")
case "$first" in
"      - uses: actions/checkout@"*"
      - name: The signed-tag gate"*) ;;
*) die "the release job's first steps are not the checkout and then the gate" ;;
esac
echo "test-verify-tag: ok, the gate is the release job's first step after the checkout"
# The release key lives in the release environment, which only this job names.
# (The checks read files, not pipes: grep -q in a pipe can stop its writer,
# which pipefail would then report.)
awk '/^  release:/ { on = 1; next } /^  [a-z]/ { on = 0 } on' "$workflow" >"$work/release-job.yml"
grep -q '^    environment: release$' "$work/release-job.yml" ||
    die "the release job does not name the release environment"
echo "test-verify-tag: ok, the release job names the release environment"
# GoReleaser builds the tag that the gate checked, not another tag on the
# same commit.
step 'goreleaser/goreleaser-action' >"$work/goreleaser-step.yml"
grep -qF 'GORELEASER_CURRENT_TAG: ${{ github.ref_name }}' "$work/goreleaser-step.yml" ||
    die "the GoReleaser step does not set GORELEASER_CURRENT_TAG from the pushed tag"
echo "test-verify-tag: ok, GoReleaser builds the pushed tag"
# The gate's step, as a script: its run block, run as GitHub runs one.
step 'name: The signed-tag gate' >"$work/gate-step.yml"
awk '
    run && !done && (/^          / || $0 == "") { print substr($0, 11); next }
    run { done = 1 }
    /^        run: \|$/ { run = 1 }
' "$work/gate-step.yml" >"$work/gate-step.sh"
grep -q 'verify-tag.sh' "$work/gate-step.sh" || die "release.yml has no signed-tag gate step with a run block"

# --- Keys, origin and the job's checkout.

# Three keys with no passphrase: one on main's list, one on no list, and one
# that a branch adds to its own copy of the list.
for k in listed other added; do
    ssh-keygen -q -t ed25519 -N '' -C "$k" -f "$work/$k"
done
signer() { # signer <key>: its line in .github/tag-signers
    printf 'maintainer@example.com namespaces="git" %s\n' "$(cut -d' ' -f1,2 "$work/$1.pub")"
}

o() { git -C "$work/origin" "$@"; }
commit() { # commit <message>: a new commit on the current branch of origin
    echo "$1" >>"$work/origin/file"
    o add -A
    o commit -q -m "$1"
}
sign() { # sign <key> <tag> [<object>]: a signed annotated tag, at origin's HEAD
    o -c gpg.format=ssh -c user.signingkey="$work/$1" tag -s -f -m "$2" "$2" "${3:-HEAD}" >/dev/null
}
list() { # list <line>...: main's .github/tag-signers, after a comment line
    { echo "# The keys that may sign a release tag."; printf '%s\n' "$@"; } >"$work/origin/.github/tag-signers"
    commit "a new list"
}

# The test cannot make a hardware key, so main's copy of the gate also takes
# plain ed25519 keys. That is the only change, and the hardware rule has
# cases of its own below.
git init -q -b main "$work/origin"
mkdir "$work/origin/.github" "$work/origin/scripts"
sed "s/^key_types='/key_types='ssh-ed25519|/" "$gate" >"$work/gate-plain.sh"
[ "$(diff "$gate" "$work/gate-plain.sh" | grep -c '^[<>]')" = 2 ] || die "cannot add plain keys to the gate's key_types"
cp "$work/gate-plain.sh" "$work/origin/scripts/verify-tag.sh"
list "$(signer listed)"

checkout() { # checkout <tag>: the release job's checkout of the tag
    rm -rf "$work/job"
    git init -q -b job "$work/job"
    git -C "$work/job" remote add origin "$work/origin"
    git -C "$work/job" fetch -q origin '+refs/heads/*:refs/remotes/origin/*' '+refs/tags/*:refs/tags/*'
    git -C "$work/job" checkout -q --detach "refs/tags/$1"
}
run() { # run <tag>: the gate's step from release.yml, in the job's checkout
    (cd "$work/job" && TAG=$1 bash --noprofile --norc -eo pipefail "$work/gate-step.sh")
}
alone() { # alone <script> [<argument>]: a copy of the gate on its own, in the job's checkout
    local script=$1
    shift
    (cd "$work/job" && bash "$script" "$@")
}
pass() { # pass <what> <tag>
    run "$2" >"$work/out" 2>&1 || die "the gate refused $1:"
    echo "test-verify-tag: ok, $1 passes"
}
must_fail() { # must_fail <what> <message> <command>...: it fails with the message and a Try line
    local what=$1 message=$2
    shift 2
    if "$@" >"$work/out" 2>&1; then
        die "the gate passed $what"
    fi
    grep -qF -- "release: $message" "$work/out" && grep -q '^Try: ' "$work/out" ||
        die "wrong error for $what:"
    echo "test-verify-tag: ok, $what fails"
}
refuse() { # refuse <what> <tag> <message>: the gate's step refuses the tag
    must_fail "$1" "$3" run "$2"
}

# Stand-ins that accept anything: a gpg that calls every signature good, and
# an ssh-keygen that does the same for SSH signatures.
mkdir "$work/fake"
cat >"$work/fake/gpg" <<'EOF'
#!/bin/sh
cat >/dev/null
echo '[GNUPG:] NEWSIG'
echo '[GNUPG:] GOODSIG 0123456789ABCDEF Maintainer'
echo '[GNUPG:] TRUST_ULTIMATE 0 pgp'
EOF
cat >"$work/fake/ssh-keygen" <<'EOF'
#!/bin/sh
case "$*" in
*find-principals*) echo maintainer@example.com ;;
*) cat >/dev/null; echo 'Good "git" signature for maintainer@example.com with ED25519 key SHA256:stand-in' ;;
esac
EOF
chmod +x "$work/fake/gpg" "$work/fake/ssh-keygen"
printf '[gpg "ssh"]\n\tprogram = %s\n' "$work/fake/ssh-keygen" >"$work/fake.gitconfig"
pgp=$(printf '%s\n' '-----BEGIN PGP SIGNATURE-----' '' 'AAAA' '-----END PGP SIGNATURE-----')

# --- The cases of spec 20.7.

# 1. A tag signed by a listed key passes.
sign listed v1.0.0
checkout v1.0.0
[ -z "$(git -C "$work/job" for-each-ref refs/heads)" ] || die "the job's checkout has a local branch"
pass "a tag signed by a listed key" v1.0.0

# 2. An unsigned annotated tag fails, and so does a lightweight tag. A tag
# whose last block is a PGP signature fails even with a gpg that calls it
# good, and so does one with an SSH block before that PGP block.
o tag -a -m v1.0.1 v1.0.1
checkout v1.0.1
refuse "an unsigned annotated tag" v1.0.1 "v1.0.1 is not signed by a key in .github/tag-signers on main."
o tag -a -f -m "$(printf 'v1.0.1\n\n%s' "$pgp")" v1.0.1 >/dev/null
checkout v1.0.1
PATH="$work/fake:$PATH" refuse "a PGP signature block" v1.0.1 "v1.0.1 is not signed by a key in .github/tag-signers on main."
o tag -a -m "$(printf 'v1.0.11\n\n-----BEGIN SSH SIGNATURE-----\n-----END SSH SIGNATURE-----\n%s' "$pgp")" v1.0.11
checkout v1.0.11
PATH="$work/fake:$PATH" refuse "an SSH block before a PGP block" v1.0.11 "v1.0.11 is not signed by a key in .github/tag-signers on main."
o tag v1.0.2
checkout v1.0.2
refuse "a lightweight tag" v1.0.2 "v1.0.2 is a lightweight tag, and a release needs a signed annotated tag."
grep -qF "Try: git tag -s -f -m v1.0.2 -- v1.0.2 $(o rev-parse HEAD), then git push --force origin refs/tags/v1.0.2." "$work/out" ||
    die "the hint for a lightweight tag does not name its commit:"

# 3. A tag signed by a key that is not listed fails, also when a git config
# names an ssh-keygen that calls every signature good.
sign other v1.0.3
checkout v1.0.3
refuse "a tag signed by a key that is not listed" v1.0.3 "v1.0.3 is not signed by a key in .github/tag-signers on main."
GIT_CONFIG_GLOBAL="$work/fake.gitconfig" refuse "a key that is not listed, with another ssh-keygen in the git config" v1.0.3 \
    "v1.0.3 is not signed by a key in .github/tag-signers on main."

# 4. A tag whose own commit adds its key to .github/tag-signers fails,
# because the file comes from main. So does a tag whose own commit replaces
# the gate, because the job runs main's copy.
o switch -q -c add-key main
signer added >>"$work/origin/.github/tag-signers"
commit "add a key"
sign added v1.0.4
o switch -q main
checkout v1.0.4
grep -qF "$(cut -d' ' -f2 "$work/added.pub")" "$work/job/.github/tag-signers" || die "the branch did not add its key"
refuse "a tag whose commit adds its own key" v1.0.4 "v1.0.4 is not signed by a key in .github/tag-signers on main."
o switch -q -c own-gate main
printf '#!/bin/sh\nexit 0\n' >"$work/origin/scripts/verify-tag.sh"
commit "a gate that passes everything"
o tag v1.0.10
o switch -q main
checkout v1.0.10
grep -qx 'exit 0' "$work/job/scripts/verify-tag.sh" || die "the branch did not replace the gate"
refuse "a tag whose commit replaces the gate" v1.0.10 "v1.0.10 is a lightweight tag, and a release needs a signed annotated tag."

# 5. A tag on a commit that is not on main fails.
o switch -q -c side main
commit "a side branch"
sign listed v1.0.5
o switch -q main
checkout v1.0.5
refuse "a tag off main" v1.0.5 "the commit of v1.0.5 is not on main."

# 6. A tag that names another commit than HEAD fails: it moved after the
# job's checkout.
sign listed v1.0.6
checkout v1.0.6
commit "second"
sign listed v1.0.6
refuse "a tag that moved after the checkout" v1.0.6 "v1.0.6 now names another commit than the one this job checked out."
grep -q "^Try: the push that moved v1.0.6 starts its own release run" "$work/out" || die "the hint for a moved tag is wrong:"

# 7. A signed tag of an older release, under a new name, fails.
o update-ref refs/tags/v1.0.7 "$(o rev-parse refs/tags/v1.0.0)"
checkout v1.0.7
refuse "an old signed tag under a new name" v1.0.7 "v1.0.7 is a signed tag made for v1.0.0, under another name."
grep -qF "git tag -s -f -m v1.0.7 -- v1.0.7 $(o rev-parse 'refs/tags/v1.0.0^{commit}'), then" "$work/out" ||
    die "the hint for an old tag under a new name does not name the commit that the gate saw:"

# 8. A tag that origin does not have fails. So does an origin that refuses
# the fetch, as a private repository refuses the gate's fetch with no token:
# both the step's fetch of the gate and the gate's own fetch say why.
checkout v1.0.0
refuse "a tag that origin does not have" v9.9.9 "cannot fetch main and v9.9.9 from origin."
git -C "$work/job" remote set-url origin "$work/refused"
refuse "an origin that refuses the step's fetch" v1.0.0 \
    "cannot read scripts/verify-tag.sh from main. The gate fetches with no token, so it needs a public repository."
grep -q "^Try: .*A private fork must give the gate's fetch a read-only token" "$work/out" || die "the step's hint does not name the token:"
must_fail "an origin that refuses the gate's fetch" \
    "cannot fetch main and v1.0.0 from origin. The gate fetches with no token, so it needs a public repository." \
    alone "$work/gate-plain.sh" v1.0.0
grep -q "^Try: .*A private fork must give the gate's fetch a read-only token" "$work/out" || die "the gate's hint does not name the token:"
checkout v1.0.0

# 9. Old tools fail before anything else. Stand-ins print old versions, and
# the stand-in git runs the real one for everything else.
mkdir "$work/old"
printf '#!/bin/sh\n[ "$1" = --version ] && { echo "git version 2.33.8"; exit 0; }\nexec "%s" "$@"\n' \
    "$(command -v git)" >"$work/old/git"
chmod +x "$work/old/git"
PATH="$work/old:$PATH" refuse "a runner with git 2.33" v1.0.0 "the runner has git 2.33, and the check needs 2.34 or later."
rm "$work/old/git"
printf '#!/bin/sh\necho "OpenSSH_8.7p1, OpenSSL 1.1.1k  25 Mar 2021" >&2\n' >"$work/old/ssh"
chmod +x "$work/old/ssh"
PATH="$work/old:$PATH" refuse "a runner with OpenSSH 8.7" v1.0.0 "the runner has OpenSSH 8.7, and the check needs 8.8 or later."

# 10. Every exit says what failed and what to try: no argument, no
# temporary folder, and a signed tag of a tree, not a commit.
must_fail "no argument" "verify-tag.sh needs one argument, the tag to check." alone "$gate"
must_fail "an empty argument" "verify-tag.sh needs one argument, the tag to check." alone "$gate" ""
RUNNER_TEMP="$work/missing" must_fail "no temporary folder" "cannot make a temporary folder in $work/missing." \
    alone "$work/gate-plain.sh" v1.0.0
sign listed v1.2.0 'HEAD^{tree}'
refuse "a signed tag of a tree" v1.2.0 "v1.2.0 does not name a commit."

# 11. Hints quote every value. A tag name may hold shell syntax: git allows
# ; $( ) ` ' | and a leading "-", though no space. Each printed command, run
# by each shell here with a stand-in git that records its arguments, runs git
# once, with the tag as one argument, and runs nothing else. One of them,
# run with the real git, then makes a signed tag that the gate passes.
if git check-ref-format 'refs/tags/v1 x'; then die "git allows a space in a tag name, so test one"; fi
mkdir "$work/sandbox" "$work/stub"
cat >"$work/stub/git" <<'EOF'
#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done >>"$LOG"
printf '%s\n' --- >>"$LOG"
EOF
chmod +x "$work/stub/git"
shells="/bin/sh $(command -v bash)"
if command -v zsh >/dev/null 2>&1; then shells="$shells $(command -v zsh)"; fi
head=$(o rev-parse HEAD)
for name in 'v1;id;#' 'v1$(id)' 'v1${IFS}x' 'v1`id`' "v1'q" 'v1|id'; do
    o update-ref "refs/tags/$name" HEAD
    checkout "$name"
    refuse "a lightweight tag named $name" "$name" "$name is a lightweight tag, and a release needs a signed annotated tag."
    hint=$(sed -n 's/^Try: //p' "$work/out")
    hint=${hint%.}
    printf '%s\n' tag -s -f -m "$name" -- "$name" "$head" --- push --force origin "refs/tags/$name" --- >"$work/want"
    for shell in $shells; do
        rm -f "$work/log" "$work/err"
        for cmd in "${hint%%, then *}" "${hint#*, then }"; do
            (cd "$work/sandbox" && env -i PATH="$work/stub" LOG="$work/log" "$shell" -c "$cmd") 2>>"$work/err" ||
                die "the hint for $name fails under $shell: $cmd"
        done
        if [ -s "$work/err" ] || ! cmp -s "$work/want" "$work/log"; then
            cat "$work/err" "$work/log" >"$work/out" 2>/dev/null
            die "the hint for $name runs something else under $shell: $hint"
        fi
    done
    echo "test-verify-tag: ok, the hint for $name runs only git, with the tag as one argument"
done
printf '[gpg]\n\tformat = ssh\n[user]\n\tsigningkey = %s\n' "$work/listed" >"$work/signing.gitconfig"
name='v1;id;#'
checkout "$name"
run "$name" >"$work/out" 2>&1 && die "the gate passed the lightweight tag $name"
hint=$(sed -n 's/^Try: //p' "$work/out")
(cd "$work/origin" && GIT_CONFIG_GLOBAL="$work/signing.gitconfig" sh -c "${hint%%, then *}") >/dev/null 2>&1 ||
    die "the printed command does not sign the tag $name: ${hint%%, then *}"
pass "the tag $name, signed again by the printed command" "$name"

# 11b. A tag whose name starts with "-" cannot be made again with git tag.
# Its hint deletes it by its full ref instead. Both printed commands, run
# with the real git, delete it: the push from a clone of a copy of origin,
# and update-ref in origin, where the maintainer made it.
o update-ref refs/tags/-v1 HEAD
checkout -v1
refuse "a tag whose name starts with -" -v1 '-v1 starts with "-", and git tag cannot make a tag with that name.'
hint=$(sed -n 's/^Try: delete it by its full ref, with //p' "$work/out")
remote_cmd=${hint%% and git update-ref *}
local_cmd="git update-ref ${hint#* and git update-ref }"
local_cmd=${local_cmd%%, then choose a release tag name*}
case "$remote_cmd $local_cmd" in
"git push --delete origin refs/tags/-v1 git update-ref -d refs/tags/-v1") ;;
*) die "the hint for -v1 is not the delete commands: $hint" ;;
esac
git clone -q --bare "$work/origin" "$work/origin-copy"
git -C "$work/origin-copy" show-ref -q --verify refs/tags/-v1 || die "the copy of origin has no tag -v1"
git clone -q "$work/origin-copy" "$work/dash-clone"
(cd "$work/dash-clone" && sh -c "$remote_cmd") >"$work/out" 2>&1 || die "the printed push does not delete -v1:"
if git -C "$work/origin-copy" show-ref -q --verify refs/tags/-v1; then die "the printed push left -v1 on origin"; fi
(cd "$work/origin" && sh -c "$local_cmd") >"$work/out" 2>&1 || die "the printed update-ref does not delete -v1:"
if o show-ref -q --verify refs/tags/-v1; then die "the printed update-ref left -v1"; fi
echo "test-verify-tag: ok, the hint for -v1 deletes it, with the real git"

# 12. The git config that releasing.md gives signs a tag and checks it with no
# agent. A plain key stands in for the hardware key, at the same path.
awk '/^### Set up your tag key/ { on = 1; next } /^### / { on = 0 } on' "$root/docs/contributing/releasing.md" |
    sed -n 's/^ *\(git config [^"]*\)$/\1/p' >"$work/doc-config"
grep -qx 'git config user.signingkey ~/.ssh/id_ed25519_sk_tags' "$work/doc-config" ||
    die "releasing.md does not point user.signingkey at the key file ~/.ssh/id_ed25519_sk_tags"
mkdir -p "$HOME/.ssh"
cp "$work/listed" "$HOME/.ssh/id_ed25519_sk_tags"
cp "$work/listed.pub" "$HOME/.ssh/id_ed25519_sk_tags.pub"
git init -q -b main "$work/laptop"
(
    cd "$work/laptop"
    echo laptop >file
    git add file
    git commit -q -m laptop
    sh -e "$work/doc-config"
    git tag -s -m v9.0.0 v9.0.0
    signer listed >"$work/laptop-signers"
    git -c gpg.ssh.allowedSignersFile="$work/laptop-signers" verify-tag v9.0.0
) >"$work/out" 2>&1 || die "the git config in releasing.md does not sign and check a tag with no agent:"
echo "test-verify-tag: ok, the git config in releasing.md signs and checks a tag with no agent"

# 13. The shipped gate takes only hardware keys: main's plain key fails.
sign listed v1.1.0
checkout v1.1.0
must_fail "a plain key on main's list" 'line 2 of .github/tag-signers on main is not a hardware key with namespaces="git".' \
    alone "$gate" v1.1.0

# 14. The gate fetches main again, on its own: a key that main drops after
# the checkout no longer signs.
list
must_fail "a key that main dropped after the checkout" "v1.1.0 is not signed by a key in .github/tag-signers on main." \
    alone "$work/gate-plain.sh" v1.1.0

# 15. A list with no keys, as the repository ships it, passes nothing.
sign listed v1.1.1
checkout v1.1.1
refuse "a tag when main's list has no keys" v1.1.1 "v1.1.1 is not signed by a key in .github/tag-signers on main."

# 16. A key line with no namespace, another namespace, or more options fails.
list "maintainer@example.com $(cut -d' ' -f1,2 "$work/listed.pub")"
sign listed v1.1.2
checkout v1.1.2
refuse "a key line with no namespace" v1.1.2 'line 2 of .github/tag-signers on main is not a hardware key with namespaces="git".'
list "maintainer@example.com namespaces=\"file\" $(cut -d' ' -f1,2 "$work/listed.pub")"
sign listed v1.1.5
checkout v1.1.5
refuse "a key line for another namespace" v1.1.5 'line 2 of .github/tag-signers on main is not a hardware key with namespaces="git".'
list "maintainer@example.com cert-authority,namespaces=\"git\" $(cut -d' ' -f1,2 "$work/listed.pub")"
sign listed v1.1.3
checkout v1.1.3
refuse "a certificate authority line" v1.1.3 'line 2 of .github/tag-signers on main is not a hardware key with namespaces="git".'

# 17. The repository's own .github/tag-signers passes the shipped gate's line
# check: the tag then fails only because no key in it signed the tag.
cp "$root/.github/tag-signers" "$work/origin/.github/tag-signers"
commit "the repository's list"
sign listed v1.1.4
checkout v1.1.4
must_fail "a tag against the repository's own list" "v1.1.4 is not signed by a key in .github/tag-signers on main." \
    alone "$gate" v1.1.4

# 18. Without .github/tag-signers on main, nothing passes.
o rm -q .github/tag-signers
commit "no list"
sign listed v1.3.0
checkout v1.3.0
refuse "a tag when main has no list" v1.3.0 "main has no .github/tag-signers, so no key may sign a release."

echo "test-verify-tag: all checks passed"
