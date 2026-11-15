if begin linger; then
    if [ "$(loginctl show-user '@USER@' -p Linger --value)" = yes ]; then
        : > "$j/unchanged"
    else
        loginctl enable-linger '@USER@'
    fi
    record
fi
