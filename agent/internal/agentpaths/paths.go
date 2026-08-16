// Package agentpaths defines stable Agent installation and data paths.
package agentpaths

import (
	"path/filepath"
	"strings"
)

const defaultWindowsProgramData = `C:\ProgramData`

func DefaultConfigPath(goos, programData string) string {
	if goos != "windows" {
		return "/etc/ace-it-center/agent.json"
	}
	if programData == "" {
		programData = defaultWindowsProgramData
	}
	return joinWithSeparator(programData, `\`, "AceITCenter", "agent.json")
}

func AgentLogPath(configPath string) string {
	return joinLike(configPath, directory(configPath), "logs", "agent.log")
}

func UpdateLogPath(configPath string) string {
	return joinLike(configPath, directory(configPath), "logs", "update.log")
}

func StagingDirectory(configPath string) string {
	return joinLike(configPath, directory(configPath), "updates")
}

func UpdaterPath(agentExecutable string) string {
	return joinLike(agentExecutable, directory(agentExecutable), "AceAgentUpdater.exe")
}

func PendingUpdaterPath(agentExecutable string) string {
	return joinLike(agentExecutable, directory(agentExecutable), "AceAgentUpdater.next.exe")
}

func directory(path string) string {
	if strings.Contains(path, `\`) {
		if index := strings.LastIndex(path, `\`); index >= 0 {
			return path[:index]
		}
	}
	return filepath.Dir(path)
}

func joinLike(reference, base string, elements ...string) string {
	if strings.Contains(reference, `\`) {
		return joinWithSeparator(base, `\`, elements...)
	}
	parts := append([]string{base}, elements...)
	return filepath.Join(parts...)
}

func joinWithSeparator(base, separator string, elements ...string) string {
	result := strings.TrimRight(base, `/\`)
	for _, element := range elements {
		result += separator + strings.Trim(element, `/\`)
	}
	return result
}
