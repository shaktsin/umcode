#!/bin/sh
set -eu
test "$(cat notes.txt)" = "evaluated"
