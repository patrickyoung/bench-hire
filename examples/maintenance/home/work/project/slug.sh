#!/bin/sh
# Starter with known bugs: punctuation, repeated spaces, and empty input.
printf '%s\n' "${1-}" | tr '[:upper:]' '[:lower:]' | tr ' ' '-'
