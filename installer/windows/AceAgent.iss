#ifndef AppVersion
  #error AppVersion must be supplied with /DAppVersion
#endif
#ifndef SourceExe
  #error SourceExe must be supplied with /DSourceExe
#endif
#ifndef SourceUpdater
  #error SourceUpdater must be supplied with /DSourceUpdater
#endif
#ifndef WindowsVersion
  #error WindowsVersion must be supplied with /DWindowsVersion
#endif
#ifndef OutputDir
  #error OutputDir must be supplied with /DOutputDir
#endif

[Setup]
AppId={{6D4E847C-51D9-4BEA-BD3B-ACE17C3A1001}
AppName=Ace IT Center Agent
AppVersion={#AppVersion}
AppPublisher=Ace IT Center
VersionInfoCompany=Ace IT Center
VersionInfoDescription=Ace IT Center Agent Setup
VersionInfoProductName=Ace IT Center Agent
VersionInfoProductVersion={#AppVersion}
VersionInfoVersion={#WindowsVersion}
VersionInfoCopyright=Copyright (C) 2026 Ace IT Center
VersionInfoOriginalFileName=AceAgentSetup-windows-amd64.exe
DefaultDirName={autopf}\Ace IT Center
MinVersion=10.0
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
UninstallDisplayIcon={app}\AceAgent.exe
OutputDir={#OutputDir}
OutputBaseFilename=AceAgentSetup-windows-amd64-V{#AppVersion}
Compression=lzma2
SolidCompression=yes
SetupIconFile=assets\ace-agent.ico
WizardSmallImageFile=assets\wizard-small.bmp
WizardImageFile=assets\wizard-large.bmp
CloseApplications=force
RestartApplications=no
DisableProgramGroupPage=yes

[Tasks]
Name: "purgedata"; Description: "卸载时删除 Ace IT Center Agent 配置与日志"; Flags: unchecked

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "AceAgent.exe"; Flags: ignoreversion
Source: "{#SourceExe}"; DestName: "AceAgentUpgrade.exe"; Flags: dontcopy
Source: "{#SourceUpdater}"; DestDir: "{app}"; DestName: "AceAgentUpdater.exe"; Flags: ignoreversion; Check: not IsUpdateHelperMode
Source: "{#SourceUpdater}"; DestDir: "{app}"; DestName: "AceAgentUpdater.next.exe"; Flags: ignoreversion; Check: IsUpdateHelperMode

[Registry]
Root: HKLM; Subkey: "Software\Microsoft\Windows\CurrentVersion\Run"; ValueType: string; ValueName: "AceITCenterAgentTray"; ValueData: """{app}\AceAgent.exe"" tray"; Flags: uninsdeletevalue

[Icons]
Name: "{autoprograms}\Ace IT Center Agent"; Filename: "{app}\AceAgent.exe"; Parameters: "tray --show"; IconFilename: "{app}\AceAgent.exe"

[Run]
Filename: "{app}\AceAgent.exe"; Parameters: "tray --show"; Flags: nowait postinstall skipifsilent runasoriginaluser; Check: CanStartTray

[UninstallDelete]
Type: files; Name: "{app}\AceAgentUpdater.exe"
Type: files; Name: "{app}\AceAgentUpdater.next.exe"
Type: filesandordirs; Name: "{commonappdata}\AceITCenter"; Tasks: purgedata

[Code]
const
  InstallLifecycleFailureExitCode = 16;
  ServiceProbeQueryFailureExitCode = 10;
  ServiceProbeTimeoutExitCode = 11;

var
  InstallLifecycleFailed: Boolean;
  UpgradePrepared: Boolean;
  ServiceReady: Boolean;

function ExecuteChecked(const Filename, Parameters, Operation: String;
  var ErrorMessage: String): Boolean;
var
  ResultCode: Integer;
begin
  if not Exec(Filename, Parameters, '', SW_HIDE, ewWaitUntilTerminated,
    ResultCode) then
  begin
    ErrorMessage := Operation + '：无法启动命令。';
    Log(ErrorMessage);
    Result := False;
  end
  else if ResultCode <> 0 then
  begin
    ErrorMessage := Format('%s：命令退出码 %d。', [Operation, ResultCode]);
    Log(ErrorMessage);
    Result := False;
  end
  else
    Result := True;
end;

function ExecuteAgentServiceCommand(const Action, Operation: String;
  var ErrorMessage: String): Boolean;
begin
  Result := ExecuteChecked(ExpandConstant('{app}\AceAgent.exe'),
    'service ' + Action, Operation, ErrorMessage);
end;

function StartService(var ErrorMessage: String): Boolean;
begin
  Result := ExecuteChecked(ExpandConstant('{sys}\sc.exe'),
    'start AceITCenterAgent', '启动 Ace IT Center Agent 服务', ErrorMessage);
end;

function WaitForServiceRunning(var ErrorMessage: String): Boolean;
var
  PowerShellParams: String;
  ResultCode: Integer;
begin
  PowerShellParams :=
    '-NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "' +
    '$deadline=[DateTime]::UtcNow.AddSeconds(15); ' +
    'do { try { $service=Get-Service -Name ''AceITCenterAgent'' -ErrorAction Stop } ' +
    'catch { exit 10 }; ' +
    'if ($service.Status -eq ''Running'') { exit 0 }; ' +
    'if ([DateTime]::UtcNow -ge $deadline) { exit 11 }; ' +
    'Start-Sleep -Milliseconds 250 } ' +
    'while ($true)"';
  if not Exec(ExpandConstant(
    '{sys}\WindowsPowerShell\v1.0\powershell.exe'), PowerShellParams, '',
    SW_HIDE, ewWaitUntilTerminated, ResultCode) then
  begin
    ErrorMessage := '确认 Ace IT Center Agent 服务运行状态：无法启动 PowerShell。';
    Log(ErrorMessage);
    Result := False;
  end
  else if ResultCode = ServiceProbeQueryFailureExitCode then
  begin
    ErrorMessage := '确认 Ace IT Center Agent 服务运行状态：Get-Service 查询失败。';
    Log(ErrorMessage);
    Result := False;
  end
  else if ResultCode = ServiceProbeTimeoutExitCode then
  begin
    ErrorMessage := 'Ace IT Center Agent 服务未在 15 秒内进入运行状态。';
    Log(ErrorMessage);
    Result := False;
  end
  else if ResultCode <> 0 then
  begin
    ErrorMessage := '确认 Ace IT Center Agent 服务运行状态失败。';
    Log(ErrorMessage);
    Result := False;
  end
  else
    Result := True;
end;

procedure RepairServiceAfterFailedInstall;
var
  RepairError: String;
begin
  if not FileExists(ExpandConstant('{app}\AceAgent.exe')) then
  begin
    Log('安装失败后没有可用于恢复的 Ace Agent 文件。');
    Exit;
  end;
  if not ExecuteAgentServiceCommand('install',
    '恢复 Ace IT Center Agent 服务注册', RepairError) then
  begin
    Log('安装失败后的服务注册恢复未完成。');
    Exit;
  end;
  if not StartService(RepairError) then
  begin
    Log('安装失败后的服务启动恢复未完成。');
    Exit;
  end;
  if not WaitForServiceRunning(RepairError) then
  begin
    Log('安装失败后的服务状态恢复未完成。');
    Exit;
  end;
  ServiceReady := True;
  Log('安装失败后已恢复 Ace IT Center Agent 服务。');
end;

procedure FailInstallLifecycle(const ErrorMessage: String);
begin
  InstallLifecycleFailed := True;
  Log('安装生命周期失败，Setup 将返回失败退出码并尝试恢复服务。');
  if not WizardSilent then
    MsgBox(ErrorMessage + #13#10 +
      '安装未完成，退出前将尝试恢复 Ace IT Center Agent 服务。',
      mbError, MB_OK);
end;

function CanStartTray: Boolean;
begin
  Result := not InstallLifecycleFailed;
end;

function IsUpdateHelperMode: Boolean;
var
  Index: Integer;
begin
  Result := False;
  for Index := 1 to ParamCount do
  begin
    if CompareText(ParamStr(Index), '/UPDATEHELPER') = 0 then
    begin
      Result := True;
      Exit;
    end;
  end;
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  UpgradeHelper: String;
begin
  Result := '';
  if IsUpdateHelperMode then
  begin
    Log('Update helper mode owns the Agent Service lifecycle.');
    Exit;
  end;
  ExtractTemporaryFile('AceAgentUpgrade.exe');
  UpgradeHelper := ExpandConstant('{tmp}\AceAgentUpgrade.exe');
  if ExecuteChecked(UpgradeHelper, 'service stop',
    '停止旧 Ace IT Center Agent 服务', Result) then
    UpgradePrepared := True;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  ErrorMessage: String;
begin
  if CurStep <> ssPostInstall then
    Exit;

  if IsUpdateHelperMode then
  begin
    ServiceReady := True;
    Exit;
  end;

  if not ExecuteAgentServiceCommand('install',
    '安装 Ace IT Center Agent 服务', ErrorMessage) then
  begin
    FailInstallLifecycle(ErrorMessage);
    Exit;
  end;
  if not StartService(ErrorMessage) then
  begin
    FailInstallLifecycle(ErrorMessage);
    Exit;
  end;
  if not WaitForServiceRunning(ErrorMessage) then
  begin
    FailInstallLifecycle(ErrorMessage);
    Exit;
  end;
  ServiceReady := True;
end;

procedure DeinitializeSetup;
begin
  if UpgradePrepared and not ServiceReady then
    RepairServiceAfterFailedInstall;
end;

function GetCustomSetupExitCode: Integer;
begin
  if InstallLifecycleFailed then
    Result := InstallLifecycleFailureExitCode
  else
    Result := 0;
end;

procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  ErrorMessage: String;
begin
  if CurUninstallStep <> usUninstall then
    Exit;
  if ExecuteAgentServiceCommand('uninstall',
    '停止并移除 Ace IT Center Agent 服务', ErrorMessage) then
    Exit;

  Log('卸载已中止，安装目录与 Agent 可执行文件保持不变。');
  if not UninstallSilent then
    MsgBox(ErrorMessage + #13#10 +
      '卸载已中止，安装目录与 Agent 可执行文件保持不变。',
      mbError, MB_OK);
  Abort;
end;
