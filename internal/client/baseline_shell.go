package client

// The command reads only the fixed baseline vocabulary. It returns hashes,
// never startup-file, authorised-key or crontab contents. This is advisory:
// a compromised host can lie, and no row authorises deletion or execution.
const baselineCommandV1 = `set -u
PATH=/usr/bin:/bin:/usr/sbin:/sbin
LC_ALL=C
export PATH LC_ALL
digest() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then shasum -a 256 | awk '{print $1}'
    else return 1
    fi
}
reading() {
    key=$1
    path=$2
    if [ -L "$path" ]; then printf '%s\tunknown\t\n' "$key"
    elif [ ! -e "$path" ]; then printf '%s\tabsent\t\n' "$key"
    elif [ -f "$path" ] && [ -r "$path" ]; then
        value=$(digest < "$path" 2>/dev/null) || value=''
        if [ ${#value} = 64 ]; then printf '%s\tknown\t%s\n' "$key" "$value"
        else printf '%s\tunknown\t\n' "$key"; fi
    else printf '%s\tunknown\t\n' "$key"
    fi
}
printf 'baseline-v1\n'
reading bashrc "$HOME/.bashrc"
reading profile "$HOME/.profile"
reading bash_profile "$HOME/.bash_profile"
reading zshrc "$HOME/.zshrc"
reading fish_config "$HOME/.config/fish/config.fish"
reading authorized_keys "$HOME/.ssh/authorized_keys"
reading ssh_rc "$HOME/.ssh/rc"
p="$HOME/.config/systemd/user"
if [ -L "$p" ]; then printf 'systemd_user\tunknown\t\n'
elif [ ! -e "$p" ]; then printf 'systemd_user\tabsent\t\n'
elif [ -d "$p" ] && entries=$(ls -1Ab "$p" 2>/dev/null); then
    value=$(printf '%s\n' "$entries" | digest) || value=''
    if [ ${#value} = 64 ]; then printf 'systemd_user\tknown\t%s\n' "$value"; else printf 'systemd_user\tunknown\t\n'; fi
else printf 'systemd_user\tunknown\t\n'
fi
if entries=$(crontab -l 2>/dev/null); then
    value=$(printf '%s\n' "$entries" | digest) || value=''
    if [ ${#value} = 64 ]; then printf 'crontab\tknown\t%s\n' "$value"; else printf 'crontab\tunknown\t\n'; fi
else printf 'crontab\tunknown\t\n'
fi
`
