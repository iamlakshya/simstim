if begin kvm-acl; then
    command -v setfacl >/dev/null || fail 'setfacl is missing, install package acl by hand'
    [ -c /dev/kvm ] || fail '/dev/kvm is missing'
    setfacl -m 'u:@USER@:rw' /dev/kvm
    record
fi
