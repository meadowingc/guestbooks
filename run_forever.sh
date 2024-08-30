#!/usr/bin/env bash

# run forever, even if we fail
while true; do
    git pull
    go build -tags release -o guestbooks
    ./guestbooks
    sleep 1
done