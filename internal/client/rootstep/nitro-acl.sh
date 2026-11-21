if begin nitro-acl; then
    command -v setfacl >/dev/null || fail 'setfacl is missing, install package acl by hand'
    command -v getfacl >/dev/null || fail 'getfacl is missing, install package acl by hand'
    [ ! -L /dev/nitro_enclaves ] && [ -c /dev/nitro_enclaves ] || fail 'the Nitro character device is missing'
    getfacl -cp /dev/nitro_enclaves | awk -F: -v n="$u" '$1 == "user" && $2 == n { print $1 ":" $2 ":" $3 }' > "$j/acl.before"
    setfacl -m 'u:@USER@:rw' /dev/nitro_enclaves
    record
fi
