#!/bin/zsh
# 从同夹的 ffmpeg-8.1.tar.xz（ffmpeg.org 原包，未改一行）编出 resources/darwin-arm64/bin/ffmpeg。
# 只开音频所需，不开 --enable-gpl／--enable-nonfree，产物许可为 LGPL 2.1 or later。
set -e
cd "${0:A:h}"
W=$(mktemp -d "$TMPDIR/ff.XXXX")
tar -xf ffmpeg-8.1.tar.xz -C "$W"
cd "$W/ffmpeg-8.1"
./configure --disable-everything --disable-autodetect --disable-doc --disable-network --disable-debug \
  --disable-programs --enable-ffmpeg --disable-avdevice --disable-swscale --enable-swresample \
  --enable-static --disable-shared \
  --extra-cflags='-mmacosx-version-min=12.0' --extra-ldflags='-mmacosx-version-min=12.0' \
  --enable-protocol='file,pipe' \
  --enable-demuxer='wav,w64,aiff,flac,mp3,mov,ogg,caf,aac,matroska,pcm_s16le' \
  --enable-decoder='pcm_s8,pcm_u8,pcm_s16le,pcm_s16be,pcm_s24le,pcm_s24be,pcm_s32le,pcm_s32be,pcm_f32le,pcm_f32be,pcm_f64le,pcm_f64be,pcm_alaw,pcm_mulaw,adpcm_ima_wav,adpcm_ms,flac,mp3,mp3float,mp2,aac,aac_latm,alac,vorbis,opus' \
  --enable-encoder='pcm_s16le,pcm_s24le,pcm_s16be,pcm_s24be,pcm_f32le,flac,aac,alac' \
  --enable-muxer='wav,aiff,flac,ipod,mp4,mov,pcm_s16le,f32le' \
  --enable-parser='flac,mpegaudio,aac,vorbis,opus' \
  --enable-bsf=aac_adtstoasc \
  --enable-filter='aresample,aformat,anull,abuffer,abuffersink,volume,atrim,format,null'
make -j"$(sysctl -n hw.ncpu)" ffmpeg
strip ffmpeg
cp ffmpeg "${0:A:h}/../../resources/darwin-arm64/bin/ffmpeg"
rm -rf "$W"
