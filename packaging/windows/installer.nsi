; E5 Developer Extend installer: per-user, no administrator rights. Built by
; scripts/release/windows.sh, which passes:
;   /DVERSION=1.2.3          numeric version for the file properties
;   /DDISPLAY_VERSION=...    version shown to the user (may be a pre-release)
;   /DARCH=amd64|arm64
;   /DSRC=<dir>              e5extend-gui.exe, e5extend.exe, e5extend-bg.exe,
;                            LICENSE.txt, MicrosoftEdgeWebview2Setup.exe
;   /DOUTFILE=<installer path>
;
; Long-running notes (docs/design/release.md, section 3.5):
; - Upgrades stop the tray app and the scheduled task first (their files are
;   locked while they run) and start the task again afterwards.
; - Uninstall removes the scheduled task through `e5extend service uninstall`
;   and the login item, but keeps the user's config, vault and logs.

Unicode true
ManifestDPIAware true
SetCompressor /SOLID lzma
RequestExecutionLevel user

!include "MUI2.nsh"
!include "x64.nsh"
!include "WinVer.nsh"
!include "LogicLib.nsh"
!include "FileFunc.nsh"

!define PRODUCT "E5 Developer Extend"
!define COMPANY "KKazuhaK"
!define GUI_EXE "e5extend-gui.exe"
!define TASK_NAME "e5extend"
; The login item's Run value (autostartID in gui/shared.go).
!define RUN_VALUE "io.github.kkazuhak.e5extend.tray"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\io.github.kkazuhak.e5extend"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define WV2_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

Name "${PRODUCT}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\${PRODUCT}"
InstallDirRegKey HKCU "${UNINST_KEY}" "InstallLocation"
BrandingText "${PRODUCT} ${DISPLAY_VERSION}"
ShowInstDetails show
ShowUninstDetails show

VIProductVersion "${VERSION}.0"
VIFileVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${PRODUCT}"
VIAddVersionKey "CompanyName" "${COMPANY}"
VIAddVersionKey "FileDescription" "${PRODUCT} Setup"
VIAddVersionKey "ProductVersion" "${DISPLAY_VERSION}"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "MIT License"

!define MUI_ICON "..\icons\icon.ico"
!define MUI_UNICON "..\icons\icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${GUI_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "$(RunNow)"
!define MUI_FINISHPAGE_SHOWREADME "https://github.com/KKazuhaK/e5-developer-extend/blob/main/docs/user/getting-started.md"
!define MUI_FINISHPAGE_SHOWREADME_TEXT "$(OpenGuide)"
!define MUI_FINISHPAGE_SHOWREADME_NOTCHECKED
!define MUI_UNFINISHPAGE_NOAUTOCLOSE

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${SRC}\LICENSE.txt"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"

LangString RunNow ${LANG_ENGLISH} "Start ${PRODUCT} (tray icon)"
LangString RunNow ${LANG_SIMPCHINESE} "启动 ${PRODUCT}（托盘图标）"
LangString OpenGuide ${LANG_ENGLISH} "Open the getting started guide"
LangString OpenGuide ${LANG_SIMPCHINESE} "打开上手指南"
LangString NeedWin10 ${LANG_ENGLISH} "${PRODUCT} needs Windows 10 or later."
LangString NeedWin10 ${LANG_SIMPCHINESE} "${PRODUCT} 需要 Windows 10 或更新的版本。"
LangString WrongArch ${LANG_ENGLISH} "This installer is for ${ARCH} Windows. Download the installer for your processor."
LangString WrongArch ${LANG_SIMPCHINESE} "这个安装包适用于 ${ARCH} 版 Windows，请下载与你的处理器对应的安装包。"
LangString Stopping ${LANG_ENGLISH} "Stopping the running ${PRODUCT} programs"
LangString Stopping ${LANG_SIMPCHINESE} "正在停止运行中的 ${PRODUCT} 程序"
LangString Restarting ${LANG_ENGLISH} "Starting the background service again"
LangString Restarting ${LANG_SIMPCHINESE} "正在重新启动后台服务"
LangString WebView ${LANG_ENGLISH} "Installing the Microsoft Edge WebView2 Runtime"
LangString WebView ${LANG_SIMPCHINESE} "正在安装 Microsoft Edge WebView2 运行时"
LangString RemovingService ${LANG_ENGLISH} "Removing the background service"
LangString RemovingService ${LANG_SIMPCHINESE} "正在删除后台服务"
LangString DataKept ${LANG_ENGLISH} "Your settings, keys and logs are kept in $APPDATA\e5extend and $LOCALAPPDATA\e5extend. Delete these folders to remove them."
LangString DataKept ${LANG_SIMPCHINESE} "你的配置、密钥和日志保留在 $APPDATA\e5extend 和 $LOCALAPPDATA\e5extend。要彻底清除，请手动删除这两个文件夹。"

Var TaskExisted
Var GuiWasRunning

Function .onInit
	SetShellVarContext current
	${IfNot} ${AtLeastWin10}
		IfSilent +2
		MessageBox MB_OK|MB_ICONSTOP "$(NeedWin10)"
		SetErrorLevel 64
		Abort
	${EndIf}
	!if "${ARCH}" == "arm64"
		${IfNot} ${IsNativeARM64}
			Goto wrongArch
		${EndIf}
	!else
		${IfNot} ${IsNativeAMD64}
			Goto wrongArch
		${EndIf}
	!endif
	Return
wrongArch:
	IfSilent +2
	MessageBox MB_OK|MB_ICONSTOP "$(WrongArch)"
	SetErrorLevel 65
	Abort
FunctionEnd

Function un.onInit
	SetShellVarContext current
FunctionEnd

; StopRunning ends the tray app and the background task so their files can be
; replaced. It records what was running so the installer can start it again.
!macro StopRunning un
Function ${un}StopRunning
	DetailPrint "$(Stopping)"
	StrCpy $TaskExisted "0"
	nsExec::Exec 'schtasks /Query /TN "${TASK_NAME}"'
	Pop $0
	${If} $0 == "0"
		StrCpy $TaskExisted "1"
		; Disable first: the task has a 5-minute repeat trigger and restarts on
		; failure, either of which could start it again while files are copied.
		nsExec::Exec 'schtasks /Change /TN "${TASK_NAME}" /DISABLE'
		Pop $0
		nsExec::Exec 'schtasks /End /TN "${TASK_NAME}"'
		Pop $0
	${EndIf}
	StrCpy $GuiWasRunning "0"
	nsExec::ExecToStack 'tasklist /FI "IMAGENAME eq ${GUI_EXE}" /NH'
	Pop $0
	Pop $1
	${If} $0 == "0"
		Push $1
		Push "${GUI_EXE}"
		Call ${un}StrContains
		Pop $2
		${If} $2 == "1"
			StrCpy $GuiWasRunning "1"
		${EndIf}
	${EndIf}
	; Every window process and the tray; the tray may be hosting the engine,
	; which writes its state atomically, so a hard stop loses nothing.
	nsExec::Exec 'taskkill /F /IM "${GUI_EXE}"'
	Pop $0
	nsExec::Exec 'taskkill /F /IM e5extend-bg.exe'
	Pop $0
	Sleep 1500
FunctionEnd
!macroend
!insertmacro StopRunning ""
!insertmacro StopRunning "un."

; StrContains pushes "1" when the string below the top of the stack contains
; the top of the stack, else "0".
!macro StrContains un
Function ${un}StrContains
	Exch $R1 ; needle
	Exch
	Exch $R0 ; haystack
	Push $R2
	Push $R3
	Push $R4
	StrLen $R2 $R1
	StrCpy $R3 0
	StrCpy $R4 "0"
	${Do}
		StrCpy $0 $R0 $R2 $R3
		${If} $0 == ""
			${ExitDo}
		${EndIf}
		${If} $0 == $R1
			StrCpy $R4 "1"
			${ExitDo}
		${EndIf}
		IntOp $R3 $R3 + 1
	${Loop}
	StrCpy $0 $R4
	Pop $R4
	Pop $R3
	Pop $R2
	Pop $R0
	Pop $R1
	Push $0
FunctionEnd
!macroend
!insertmacro StrContains ""
!insertmacro StrContains "un."

Function EnableTask
	nsExec::Exec 'schtasks /Change /TN "${TASK_NAME}" /ENABLE'
	Pop $0
FunctionEnd

; A failed or cancelled upgrade must not leave the background task disabled.
Function .onInstFailed
	${If} $TaskExisted == "1"
		Call EnableTask
	${EndIf}
FunctionEnd

Section "Install"
	SetShellVarContext current
	Call StopRunning

	SetOutPath "$INSTDIR"
	File "${SRC}\${GUI_EXE}"
	File "${SRC}\e5extend.exe"
	File "${SRC}\e5extend-bg.exe"
	File "${SRC}\LICENSE.txt"

	; WebView2 is part of Windows 11 and of almost every Windows 10; install it
	; with Microsoft's bootstrapper only when neither registration exists.
	SetRegView 64
	ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WV2_GUID}" "pv"
	ReadRegStr $1 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WV2_GUID}" "pv"
	SetRegView lastused
	${If} $0 == ""
	${AndIf} $1 == ""
		DetailPrint "$(WebView)"
		InitPluginsDir
		SetOutPath "$PLUGINSDIR"
		File "${SRC}\MicrosoftEdgeWebview2Setup.exe"
		ExecWait '"$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install'
		SetOutPath "$INSTDIR"
	${EndIf}

	CreateShortcut "$SMPROGRAMS\${PRODUCT}.lnk" "$INSTDIR\${GUI_EXE}" "" "$INSTDIR\${GUI_EXE}" 0

	WriteUninstaller "$INSTDIR\uninstall.exe"
	WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "${PRODUCT}"
	WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${DISPLAY_VERSION}"
	WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "${COMPANY}"
	WriteRegStr HKCU "${UNINST_KEY}" "URLInfoAbout" "https://github.com/KKazuhaK/e5-developer-extend"
	WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${GUI_EXE}"
	WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
	WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
	WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
	WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
	WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
	${GetSize} "$INSTDIR" "/S=0K" $0 $1 $2
	IntFmt $0 "0x%08X" $0
	WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" "$0"

	; The task runs $INSTDIR\e5extend-bg.exe, which was just replaced.
	${If} $TaskExisted == "1"
		DetailPrint "$(Restarting)"
		Call EnableTask
		nsExec::Exec 'schtasks /Run /TN "${TASK_NAME}"'
		Pop $0
	${EndIf}
	; A silent upgrade has no finish page, so start the tray again here.
	${If} $GuiWasRunning == "1"
	${AndIf} ${Silent}
		Exec '"$INSTDIR\${GUI_EXE}"'
	${EndIf}
SectionEnd

Section "Uninstall"
	SetShellVarContext current
	Call un.StopRunning
	DetailPrint "$(RemovingService)"
	; Idempotent: reports "nothing to remove" when no task is installed.
	nsExec::ExecToLog '"$INSTDIR\e5extend.exe" service uninstall'
	Pop $0

	DeleteRegValue HKCU "${RUN_KEY}" "${RUN_VALUE}"
	Delete "$SMPROGRAMS\${PRODUCT}.lnk"
	Delete "$INSTDIR\${GUI_EXE}"
	Delete "$INSTDIR\e5extend.exe"
	Delete "$INSTDIR\e5extend-bg.exe"
	Delete "$INSTDIR\LICENSE.txt"
	Delete "$INSTDIR\uninstall.exe"
	RMDir "$INSTDIR"
	; The web view cache of the window (not user data).
	RMDir /r "$APPDATA\${GUI_EXE}"
	DeleteRegKey HKCU "${UNINST_KEY}"

	DetailPrint "$(DataKept)"
SectionEnd
