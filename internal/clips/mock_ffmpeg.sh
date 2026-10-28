#!/bin/sh
for arg in "$@"; do
    case "$arg" in
        *.mp4|*.mkv)
            echo "mock data" > "$arg"
            ;;
    esac
done
echo "out_time_us=1000000"
echo "out_time_us=2000000"
exit 0
