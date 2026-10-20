!include LogicLib.nsh

!macro customInit
  IfFileExists "$EXEDIR\Shuji-Rongbao-Offline-Models-${VERSION}.json" 0 missingModelManifest
  IfFileExists "$EXEDIR\Shuji-Rongbao-Offline-Models-${VERSION}.part01.bin" 0 missingModelParts
  IfFileExists "$EXEDIR\Shuji-Rongbao-Offline-Models-${VERSION}.part02.bin" 0 missingModelParts
  IfFileExists "$EXEDIR\Shuji-Rongbao-Offline-Models-${VERSION}.part03.bin" 0 missingModelParts
  IfFileExists "$EXEDIR\Shuji-Rongbao-Offline-Models-${VERSION}.part04.bin" 0 missingModelParts
  Goto modelFilesReady
missingModelManifest:
  MessageBox MB_ICONSTOP "模型校验清单不在安装器旁边。请将完整离线安装包的所有文件放进同一文件夹后再运行。"
  Abort
missingModelParts:
  MessageBox MB_ICONSTOP "缺少模型分卷。请将完整离线安装包的所有文件放进同一文件夹后再运行。"
  Abort
modelFilesReady:
!macroend

!macro customInstall
  DetailPrint "正在校验并安装离线 AI 模型，请保持安装窗口打开。"
  ExecWait '"$INSTDIR\resources\offline-runtime\unpack-models.exe" --manifest "$EXEDIR\Shuji-Rongbao-Offline-Models-${VERSION}.json" --source "$EXEDIR" --target "$INSTDIR\resources\offline-runtime\ollama"' $0

  ${If} $0 != 0
    MessageBox MB_ICONSTOP "离线模型安装失败。请确认安装磁盘有足够空间，然后重新运行安装器。"
    Abort
  ${EndIf}
!macroend
