#!/bin/sh

if [ "$#" -eq 0 ] || [ -z "$1" ]; then
    printf '%s\n' 'slug: input must contain an ASCII letter or digit' >&2
    exit 2
fi

slug=$(
    printf '%s\n' "$1" |
        LC_ALL=C tr 'ABCDEFGHIJKLMNOPQRSTUVWXYZ' 'abcdefghijklmnopqrstuvwxyz' |
        LC_ALL=C tr -cs 'a-z0-9' '-' |
        LC_ALL=C sed 's/^-//; s/-$//'
)

if [ -z "$slug" ]; then
    printf '%s\n' 'slug: input must contain an ASCII letter or digit' >&2
    exit 2
fi

printf '%s\n' "$slug"
