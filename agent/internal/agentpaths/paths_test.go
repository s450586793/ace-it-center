package agentpaths

import "testing"

func TestWindowsAgentPathsAreStable(t *testing.T) {
	config := DefaultConfigPath("windows", `C:\ProgramData`)
	if config != `C:\ProgramData\AceITCenter\agent.json` {
		t.Fatalf("config path = %q", config)
	}
	if got := AgentLogPath(config); got != `C:\ProgramData\AceITCenter\logs\agent.log` {
		t.Fatalf("agent log path = %q", got)
	}
	if got := UpdateLogPath(config); got != `C:\ProgramData\AceITCenter\logs\update.log` {
		t.Fatalf("update log path = %q", got)
	}
	if got := StagingDirectory(config); got != `C:\ProgramData\AceITCenter\updates` {
		t.Fatalf("staging path = %q", got)
	}
	agent := `C:\Program Files\Ace IT Center\AceAgent.exe`
	if got := UpdaterPath(agent); got != `C:\Program Files\Ace IT Center\AceAgentUpdater.exe` {
		t.Fatalf("updater path = %q", got)
	}
	if got := PendingUpdaterPath(agent); got != `C:\Program Files\Ace IT Center\AceAgentUpdater.next.exe` {
		t.Fatalf("pending updater path = %q", got)
	}
}

func TestDefaultConfigPathUsesWindowsFallbackAndLinuxLocation(t *testing.T) {
	if got := DefaultConfigPath("windows", ""); got != `C:\ProgramData\AceITCenter\agent.json` {
		t.Fatalf("Windows fallback = %q", got)
	}
	if got := DefaultConfigPath("linux", "/ignored"); got != "/etc/ace-it-center/agent.json" {
		t.Fatalf("Linux config = %q", got)
	}
}

func TestPOSIXAgentPathsAreStable(t *testing.T) {
	config := "/etc/ace-it-center/agent.json"
	if got := AgentLogPath(config); got != "/etc/ace-it-center/logs/agent.log" {
		t.Fatalf("agent log path = %q", got)
	}
	if got := UpdateLogPath(config); got != "/etc/ace-it-center/logs/update.log" {
		t.Fatalf("update log path = %q", got)
	}
	if got := StagingDirectory(config); got != "/etc/ace-it-center/updates" {
		t.Fatalf("staging path = %q", got)
	}
	if got := UpdaterPath("/opt/ace/AceAgent"); got != "/opt/ace/AceAgentUpdater.exe" {
		t.Fatalf("updater path = %q", got)
	}
}
