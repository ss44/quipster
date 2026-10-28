#!/bin/sh
# Variant ffmpeg mock driven by environment variables, used by the clips
# tests to exercise specific render paths deterministically:
#
#   MOCK_FAIL_ALWAYS=1      every invocation exits 1
#   MOCK_FAIL_MP4=1         invocations whose output ends in .mp4 exit 1
#                           (mkv outputs proceed, covering the mkv fallback)
#   MOCK_FAIL_FIRST=1       the first invocation exits 1, later ones succeed
#                           (counter in MOCK_COUNTER_FILE, covers vaapi
#                           failures that retry on cpu)
#   MOCK_SLEEP=N            sleep N seconds before succeeding (flight dedup)
out=""
for arg in "$@"; do
	case "$arg" in
		*.mp4|*.mkv) out="$arg";;
	esac
done

if [ "$MOCK_FAIL_ALWAYS" = "1" ]; then
	exit 1
fi
if [ "$MOCK_FAIL_MP4" = "1" ]; then
	case "$out" in
		*.mp4) exit 1;;
	esac
fi
if [ "$MOCK_FAIL_FIRST" = "1" ]; then
	n=0
	[ -f "$MOCK_COUNTER_FILE" ] && read -r n < "$MOCK_COUNTER_FILE"
	n=$((n+1))
	echo "$n" > "$MOCK_COUNTER_FILE"
	if [ "$n" = "1" ]; then exit 1; fi
fi
if [ -n "$MOCK_SLEEP" ]; then
	sleep "$MOCK_SLEEP"
fi

for arg in "$@"; do
	case "$arg" in
		*.mp4|*.mkv) echo "mock data" > "$arg";;
	esac
done
echo "out_time_us=1000000"
echo "out_time_us=2000000"
exit 0
