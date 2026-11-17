; SPDX-License-Identifier: GPL-3.0-or-later
; First-install package for Oddward (Inno Setup 6). It lays out
;   {app}\Oddward.exe          the stub that picks a version
;   {app}\app-<version>\       the launcher itself
; Later versions arrive through the launcher's own updater as new app-*
; folders; this installer is only needed once.
;
; iscc /DAppVersion=0.1.0 /DStageDir=<dir with Oddward.exe and app-0.1.0> /O<out> installer\oddward.iss

#ifndef AppVersion
  #error Pass /DAppVersion=X.Y.Z
#endif
#ifndef StageDir
  #error Pass /DStageDir=<release stage folder>
#endif

#define ChineseMessages AddBackslash(CompilerPath) + "Languages\ChineseSimplified.isl"

[Setup]
AppId={{6F1D2C7E-4B3A-4E58-9C61-0DDA7A2B5E10}
AppName=Oddward
AppVersion={#AppVersion}
AppPublisher=Oddward contributors
AppPublisherURL=https://github.com/nerv-base/Oddward
AppSupportURL=https://github.com/nerv-base/Oddward/issues
DefaultDirName={localappdata}\Programs\Oddward
DisableProgramGroupPage=yes
DisableDirPage=auto
PrivilegesRequired=lowest
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
MinVersion=10.0
WizardStyle=modern
Compression=lzma2/max
SolidCompression=yes
OutputBaseFilename=oddward-{#AppVersion}-setup
UninstallDisplayName=Oddward
UninstallDisplayIcon={app}\Oddward.exe
CloseApplications=yes

[Languages]
#if FileExists(ChineseMessages)
Name: "chs"; MessagesFile: "{#ChineseMessages}"
#else
Name: "en"; MessagesFile: "compiler:Default.isl"
#endif

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"; Flags: unchecked

[InstallDelete]
; A setup run yields exactly its own version; stale or failed folders go.
Type: filesandordirs; Name: "{app}\app-*"
Type: filesandordirs; Name: "{app}\.updates"

[Files]
Source: "{#StageDir}\Oddward.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#StageDir}\app-{#AppVersion}\*"; DestDir: "{app}\app-{#AppVersion}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\Oddward"; Filename: "{app}\Oddward.exe"
Name: "{autodesktop}\Oddward"; Filename: "{app}\Oddward.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\Oddward.exe"; Description: "{cm:LaunchProgram,Oddward}"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
Type: filesandordirs; Name: "{app}\app-*"
Type: filesandordirs; Name: "{app}\.updates"

[Code]
procedure CurStepChanged(CurStep: TSetupStep);
begin
  if CurStep = ssPostInstall then
    { The stub only starts folders carrying the ready marker. }
    SaveStringToFile(ExpandConstant('{app}\app-{#AppVersion}\.ready'), '{#AppVersion}', False);
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
begin
  if (CurUninstallStep = usPostUninstall) and not UninstallSilent then
    if MsgBox('是否同时删除 Oddward 的设置、已保存账号、抽卡档案和缓存？' + #13#10#13#10 +
              '抽卡档案删除后无法从服务端找回超过 6 个月的记录。' + #13#10 +
              '选择“否”会保留它们，重新安装后可以继续使用。游戏目录不会被触碰。',
              mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
    begin
      DelTree(ExpandConstant('{localappdata}\Oddward'), True, True, True);
      DelTree(ExpandConstant('{userappdata}\Oddward'), True, True, True);
    end;
end;
