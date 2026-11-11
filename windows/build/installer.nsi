; MyTizi Windows 安装程序（NSIS 3，Unicode）。由 scripts/build.sh 调用：
;   makensis -DVERSION=0.4.0 -DVERSION4=0.4.0.0 -DARCH=x64 -DSTAGE=<目录> -DOUTFILE=<安装包> installer.nsi
; 可选：-DWEBVIEW2_BOOTSTRAPPER=<MicrosoftEdgeWebview2Setup.exe>，缺 WebView2 运行时的机器上静默安装它。
;
; 安装做的事：复制文件到 Program Files → 注册并启动后台服务（mytizi-service.exe install）
; → 开始菜单与桌面快捷方式 → "应用和功能"里的卸载项。升级时先停掉旧的 App 与服务。
; 卸载：mytizi-service.exe uninstall（断开、删除服务、清理 %ProgramData%\MyTizi），用户的订阅与设置保留。

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma

!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "FileFunc.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef VERSION4
  !define VERSION4 "0.0.0.0"
!endif
!ifndef ARCH
  !define ARCH "x64"
!endif

!define APPNAME "MyTizi"
!define SERVICE "MyTiziHelper"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\MyTizi"
!define WEBVIEW2KEY "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

Name "${APPNAME}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES64\${APPNAME}"
RequestExecutionLevel admin
BrandingText "${APPNAME} ${VERSION}"

VIProductVersion "${VERSION4}"
VIFileVersion "${VERSION4}"
VIAddVersionKey /LANG=2052 "ProductName" "${APPNAME}"
VIAddVersionKey /LANG=2052 "FileDescription" "${APPNAME} 安装程序"
VIAddVersionKey /LANG=2052 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "LegalCopyright" "MIT License"

!define MUI_ICON "icon.ico"
!define MUI_UNICON "icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "打开 MyTizi"
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchAsUser
!define MUI_WELCOMEPAGE_TEXT "MyTizi 是一个极简的代理客户端：粘贴订阅链接，点一下就连上。$\r$\n$\r$\n安装会同时注册一个后台服务（MyTiziHelper），它负责在你点“连接”时创建虚拟网卡；没有连接时它是空闲的。$\r$\n$\r$\n请先退出其他会开虚拟网卡（TUN）的代理软件。"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "SimpChinese"

; 以当前登录用户（而不是管理员）身份启动 App：经由 explorer.exe 打开，继承的是桌面用户的令牌。
Function LaunchAsUser
  Exec '"$WINDIR\explorer.exe" "$INSTDIR\MyTizi.exe"'
FunctionEnd

Function .onInit
  SetRegView 64
  ; 架构检查：Wintun 驱动必须与系统架构一致，x64 安装包装在 ARM 版 Windows 上无法创建虚拟网卡。
  !if "${ARCH}" == "arm64"
    ${IfNot} ${IsNativeARM64}
      MessageBox MB_ICONSTOP "这是 ARM 版 Windows 的安装包。请下载 x64 版（文件名带 windows-x64）。" /SD IDOK
      Abort
    ${EndIf}
  !else
    ${If} ${IsNativeARM64}
      MessageBox MB_ICONSTOP "这台电脑是 ARM 版 Windows，请下载 arm64 版安装包（文件名带 windows-arm64）。" /SD IDOK
      Abort
    ${EndIf}
    ${IfNot} ${RunningX64}
      MessageBox MB_ICONSTOP "MyTizi 需要 64 位 Windows 10 或更新版本。" /SD IDOK
      Abort
    ${EndIf}
  !endif
FunctionEnd

Function un.onInit
  SetRegView 64
FunctionEnd

; WebView2 运行时：Windows 11 自带，Windows 10 绝大多数已通过系统更新装好；缺了就补装。
Function EnsureWebView2
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WEBVIEW2KEY}" "pv"
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WEBVIEW2KEY}" "pv"
  ${EndIf}
  ${If} $0 != ""
  ${AndIf} $0 != "0.0.0.0"
    Return
  ${EndIf}
  !ifdef WEBVIEW2_BOOTSTRAPPER
    DetailPrint "正在安装 Microsoft Edge WebView2 运行时…"
    SetOutPath "$PLUGINSDIR"
    File "/oname=$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" "${WEBVIEW2_BOOTSTRAPPER}"
    ExecWait '"$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install' $1
    ${If} $1 != 0
      MessageBox MB_ICONEXCLAMATION "WebView2 运行时安装失败（$1）。MyTizi 的界面需要它，请稍后从微软官网下载安装。" /SD IDOK
    ${EndIf}
  !else
    MessageBox MB_ICONEXCLAMATION "这台电脑缺少 Microsoft Edge WebView2 运行时，MyTizi 的界面需要它。$\r$\n安装完成后会打开下载页面。" /SD IDOK
    ExecShell "open" "https://developer.microsoft.com/microsoft-edge/webview2/"
  !endif
FunctionEnd

Section "MyTizi" SecMain
  SetRegView 64
  SetShellVarContext all
  InitPluginsDir

  ; 升级：先关掉旧的 App（释放文件），再停服务（会断开连接，核心与虚拟网卡随之消失）。
  nsExec::Exec 'taskkill /F /IM MyTizi.exe'
  Pop $0
  ${If} ${FileExists} "$INSTDIR\mytizi-service.exe"
    DetailPrint "正在停止后台服务…"
    nsExec::Exec 'net stop ${SERVICE}'
    Pop $0
  ${EndIf}
  Sleep 500

  SetOutPath "$INSTDIR"
  File /r "${STAGE}\*.*"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  Call EnsureWebView2

  DetailPrint "正在注册后台服务…"
  nsExec::ExecToLog '"$INSTDIR\mytizi-service.exe" install'
  Pop $0
  ${If} $0 != 0
    MessageBox MB_ICONEXCLAMATION "后台服务注册失败（$0）。可以稍后在 MyTizi 里点“修复后台组件”再试。" /SD IDOK
  ${EndIf}

  CreateShortcut "$SMPROGRAMS\${APPNAME}.lnk" "$INSTDIR\MyTizi.exe" "" "$INSTDIR\MyTizi.exe" 0
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\MyTizi.exe" "" "$INSTDIR\MyTizi.exe" 0

  WriteRegStr HKLM "${UNINSTKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINSTKEY}" "Publisher" "${APPNAME}"
  WriteRegStr HKLM "${UNINSTKEY}" "URLInfoAbout" "https://github.com/kadaliao/mytizi"
  WriteRegStr HKLM "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\MyTizi.exe"
  WriteRegStr HKLM "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINSTKEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKLM "${UNINSTKEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINSTKEY}" "NoRepair" 1
  ${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
  IntFmt $0 "0x%08X" $0
  WriteRegDWORD HKLM "${UNINSTKEY}" "EstimatedSize" "$0"
SectionEnd

Section "Uninstall"
  SetRegView 64
  SetShellVarContext all

  nsExec::Exec 'taskkill /F /IM MyTizi.exe'
  Pop $0
  DetailPrint "正在断开并删除后台服务…"
  nsExec::ExecToLog '"$INSTDIR\mytizi-service.exe" uninstall'
  Pop $0
  Sleep 500

  Delete "$INSTDIR\MyTizi.exe"
  Delete "$INSTDIR\mytizi-service.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir /r "$INSTDIR\core"
  RMDir /r "$INSTDIR\licenses"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APPNAME}.lnk"
  Delete "$DESKTOP\${APPNAME}.lnk"
  DeleteRegKey HKLM "${UNINSTKEY}"
  ; 运行卸载程序的这个用户的开机启动项（其他用户的会在下次开机时因为文件不存在而失效）。
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "${APPNAME}"
SectionEnd
