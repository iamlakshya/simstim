if begin subuid; then
    /bin/sh "$d/subid-allocate.sh" "$j"
    [ -f "$j/allocator.complete" ] || fail 'the subordinate ID allocator did not complete'
    range=$(cat "$j/range")
    extra=",\"range\":\"$range\""
    record
    extra=''
fi
