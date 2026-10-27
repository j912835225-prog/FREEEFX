#!/bin/zsh
# 出两份：Mac（Apple 芯片）dmg、Windows x64。先跑 go test，不过不打。安装包放项目夹根。
# 重签：ad-hoc 签名的身份默认是 cdhash，每打一次就变；改成按 identifier 认。mygo 的 dmg 装的是重签前的 app，所以重签后自己重做 dmg。
# Windows：ffmpeg.exe 要在 FREEEFX.exe 同目录的 bin 下。resources/windows-amd64/bin/ffmpeg.exe 在，就出 zip（exe＋bin＋许可）；
# 不在就只出单个 exe——照样能用，只是右键没有「转成」，非 WAV/AIFF 不做分析。
# ./构建.sh win 只出 Windows。
set -e
cd "${0:A:h}"
go test .
rm -rf dist
P=darwin/arm64,windows/amd64; [[ $1 == win ]] && P=windows/amd64
go tool mygo build -platform $P
V=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' mygo.json)
if [[ $1 != win ]]; then
A=dist/darwin-arm64/FREEEFX.app
codesign --force --deep -s - -i app.freeefx -r='designated => identifier "app.freeefx"' $A
codesign -v --strict $A
S=$(mktemp -d "$TMPDIR/dmg.XXXX")
ditto $A "$S/FREEEFX.app" && ln -s /Applications "$S/Applications"
hdiutil create -quiet -volname FREEEFX -srcfolder "$S" -fs HFS+ -format UDZO "../FREEEFX $V.dmg" -ov
rm -rf "$S"
fi
W=dist/windows-amd64
rm -f "../FREEEFX $V windows-x64.exe" "../FREEEFX $V windows-x64.zip"
if [[ -f $W/bin/ffmpeg.exe ]]; then
  Z=$(mktemp -d "$TMPDIR/zip.XXXX")
  mkdir "$Z/FREEEFX" && cp -R $W/FREEEFX.exe $W/bin $W/ffmpeg-LICENSE-LGPLv3.txt $W/ffmpeg-LICENSE-GPLv3.txt $W/ffmpeg-NOTICE.txt "$Z/FREEEFX/" # Windows 的 ffmpeg 是 LGPL v3，不放 Mac 那份 2.1
  # 用 Python 打 zip：中文名带 UTF-8 标记，Windows 资源管理器解出来不乱码；不夹 __MACOSX
  python3 -c 'import sys,os,zipfile;z=zipfile.ZipFile(sys.argv[2],"w",zipfile.ZIP_DEFLATED);os.chdir(sys.argv[1]);[z.write(os.path.join(d,f)) for d,_,fs in os.walk("FREEEFX") for f in fs];z.close()' "$Z" "$PWD/../FREEEFX $V windows-x64.zip"
  rm -rf "$Z"
  echo "../FREEEFX $V windows-x64.zip"
else
  cp $W/FREEEFX.exe "../FREEEFX $V windows-x64.exe"
  echo "../FREEEFX $V windows-x64.exe（未带 ffmpeg.exe）"
fi
[[ $1 == win ]] || echo "../FREEEFX $V.dmg"
