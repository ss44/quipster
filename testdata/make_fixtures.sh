#!/usr/bin/env sh
# Generates small test fixtures: two sidecar-subs files, one with embedded subs, one without.
set -e
cd "$(dirname "$0")"

mk() { # out.mp4 seconds
  ffmpeg -v error -y -f lavfi -i "testsrc2=size=320x180:rate=24:duration=$2" \
    -f lavfi -i "sine=frequency=440:duration=$2" -c:v libx264 -preset veryfast -crf 30 \
    -c:a aac -b:a 64k -shortest "$1"
}

embed() { # out.mp4 srt seconds
  ffmpeg -v error -y -f lavfi -i "testsrc2=size=320x180:rate=24:duration=$3" \
    -f lavfi -i "sine=frequency=440:duration=$3" -i "$2" \
    -map 0:v -map 1:a -map 2:0 -c:v libx264 -preset veryfast -crf 30 -c:a aac -b:a 64k \
    -c:s mov_text -metadata:s:s:0 language=eng -shortest "$1"
}

mkdir -p "Movies/Big Buck Bunny (2008)" "TV Shows/Quip Show (2020)/Season 01" "Movies/Silent Movie (2019)"

cat > "Movies/Big Buck Bunny (2008)/Big Buck Bunny (2008).srt" <<'EOF'
1
00:00:00,500 --> 00:00:03,000
I want you to remember this moment.

2
00:00:03,500 --> 00:00:06,000
The day you almost captured Captain Jack.

3
00:00:06,500 --> 00:00:09,000
You talking to me? You talking to me?
EOF

cat > "TV Shows/Quip Show (2020)/Season 01/Quip Show (2020) - S01E01 - Pilot [720p].srt" <<'EOF'
1
00:00:00,500 --> 00:00:03,000
Here's looking at you, kid.

2
00:00:04,000 --> 00:00:07,500
Show me the money!
EOF

cat > /tmp/embed_subs.srt <<'EOF'
1
00:00:00,500 --> 00:00:03,000
May the Force be with you.

2
00:00:03,500 --> 00:00:07,000
I feel the need... the need for speed.
EOF

echo "1. sidecar movie…"
mk "Movies/Big Buck Bunny (2008)/Big Buck Bunny (2008).mp4" 10
echo "2. sidecar TV episode…"
mk "TV Shows/Quip Show (2020)/Season 01/Quip Show (2020) - S01E01 - Pilot [720p].mp4" 8
echo "3. embedded subs movie…"
mkdir -p "Movies/Embedded Feature (2011)"
embed "Movies/Embedded Feature (2011)/Embedded Feature (2011).mp4" /tmp/embed_subs.srt 8
echo "4. no-subs movie…"
mk "Movies/Silent Movie (2019)/Silent Movie (2019).mp4" 6

echo "fixtures ready under testdata/"