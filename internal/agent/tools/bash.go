package tools

import (
	"bytes"
	"cmp"
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/shell"
)

type BashParams struct {
	Description         string `json:"description" description:"A brief description of what the command does, try to keep it under 30 characters or so"`
	Command             string `json:"command" description:"The command to execute"`
	WorkingDir          string `json:"working_dir,omitempty" description:"The working directory to execute the command in (defaults to current directory)"`
	RunInBackground     bool   `json:"run_in_background,omitempty" description:"Set to true (boolean) to run this command in the background. Use job_output to read the output later."`
	AutoBackgroundAfter int    `json:"auto_background_after,omitempty" description:"Seconds to wait before automatically moving the command to a background job (default: 60)"`
}

type BashPermissionsParams struct {
	Description         string `json:"description"`
	Command             string `json:"command"`
	WorkingDir          string `json:"working_dir"`
	RunInBackground     bool   `json:"run_in_background"`
	AutoBackgroundAfter int    `json:"auto_background_after"`
}

type BashResponseMetadata struct {
	StartTime        int64  `json:"start_time"`
	EndTime          int64  `json:"end_time"`
	Command          string `json:"command,omitempty"`
	ExecutedCommand  string `json:"executed_command,omitempty"`
	Output           string `json:"output"`
	Stdout           string `json:"stdout,omitempty"`
	Stderr           string `json:"stderr,omitempty"`
	Description      string `json:"description"`
	WorkingDirectory string `json:"working_directory"`
	ExitCode         int    `json:"exit_code,omitempty"`
	Retryable        bool   `json:"retryable,omitempty"`
	FailureKind      string `json:"failure_kind,omitempty"`
	Background       bool   `json:"background,omitempty"`
	ShellID          string `json:"shell_id,omitempty"`
}

const (
	BashToolName = "bash"

	DefaultAutoBackgroundAfter            = 60 // Commands taking longer automatically become background jobs
	MaxOutputLength                       = 30000
	BashNoOutput                          = "no output"
	BashFailureKindWindowsPathTranslation = "windows_path_translation_failure"
	BashFailureKindWindowsShellMismatch   = "windows_shell_command_mismatch"
)

//go:embed bash.tpl
var bashDescriptionTmpl []byte

var bashDescriptionTpl = template.Must(
	template.New("bashDescription").
		Parse(string(bashDescriptionTmpl)),
)

type bashDescriptionData struct {
	BannedCommands  string
	MaxOutputLength int
	Attribution     config.Attribution
	ModelName       string
}

var bannedCommands = []string{
	// Network/Download tools
	"alias",
	"aria2c",
	"axel",
	"chrome",
	"curl",
	"curlie",
	"firefox",
	"http-prompt",
	"httpie",
	"links",
	"lynx",
	"nc",
	"safari",
	"scp",
	"ssh",
	"telnet",
	"w3m",
	"wget",
	"xh",

	// System administration
	"doas",
	"su",
	"sudo",

	// Package managers
	"apk",
	"apt",
	"apt-cache",
	"apt-get",
	"dnf",
	"dpkg",
	"emerge",
	"home-manager",
	"makepkg",
	"opkg",
	"pacman",
	"paru",
	"pkg",
	"pkg_add",
	"pkg_delete",
	"portage",
	"rpm",
	"yay",
	"yum",
	"zypper",

	// System modification
	"at",
	"batch",
	"chkconfig",
	"crontab",
	"fdisk",
	"mkfs",
	"mount",
	"parted",
	"service",
	"systemctl",
	"umount",

	// Network configuration
	"firewall-cmd",
	"ifconfig",
	"ip",
	"iptables",
	"netstat",
	"pfctl",
	"route",
	"ufw",
}

func bashDescription(attribution *config.Attribution, modelName string) string {
	bannedCommandsStr := strings.Join(bannedCommands, ", ")
	var out bytes.Buffer
	if err := bashDescriptionTpl.Execute(&out, bashDescriptionData{
		BannedCommands:  bannedCommandsStr,
		MaxOutputLength: MaxOutputLength,
		Attribution:     *attribution,
		ModelName:       modelName,
	}); err != nil {
		// this should never happen.
		panic("failed to execute bash description template: " + err.Error())
	}
	return out.String()
}

func blockFuncs() []shell.BlockFunc {
	return []shell.BlockFunc{
		shell.CommandsBlocker(bannedCommands),

		// System package managers
		shell.ArgumentsBlocker("apk", []string{"add"}, nil),
		shell.ArgumentsBlocker("apt", []string{"install"}, nil),
		shell.ArgumentsBlocker("apt-get", []string{"install"}, nil),
		shell.ArgumentsBlocker("dnf", []string{"install"}, nil),
		shell.ArgumentsBlocker("pacman", nil, []string{"-S"}),
		shell.ArgumentsBlocker("pkg", []string{"install"}, nil),
		shell.ArgumentsBlocker("yum", []string{"install"}, nil),
		shell.ArgumentsBlocker("zypper", []string{"install"}, nil),

		// Language-specific package managers
		shell.ArgumentsBlocker("brew", []string{"install"}, nil),
		shell.ArgumentsBlocker("cargo", []string{"install"}, nil),
		shell.ArgumentsBlocker("gem", []string{"install"}, nil),
		shell.ArgumentsBlocker("go", []string{"install"}, nil),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"--global"}),
		shell.ArgumentsBlocker("npm", []string{"install"}, []string{"-g"}),
		shell.ArgumentsBlocker("pip", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pip3", []string{"install"}, []string{"--user"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"--global"}),
		shell.ArgumentsBlocker("pnpm", []string{"add"}, []string{"-g"}),
		shell.ArgumentsBlocker("yarn", []string{"global", "add"}, nil),

		// `go test -exec` can run arbitrary commands
		shell.ArgumentsBlocker("go", []string{"test"}, []string{"-exec"}),
	}
}

func NewBashTool(permissions permission.Service, workingDir string, attribution *config.Attribution, modelName string) fantasy.AgentTool {
	return WithAliases(fantasy.NewAgentTool(
		BashToolName,
		string(bashDescription(attribution, modelName)),
		func(ctx context.Context, params BashParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Command == "" {
				return fantasy.NewTextErrorResponse("missing command"), nil
			}

			// Determine working directory
			execWorkingDir := cmp.Or(params.WorkingDir, workingDir)
			originalCommand := params.Command
			execCommand, execWorkingDir := normalizeBashInvocation(params.Command, execWorkingDir)

			isSafeReadOnly := false
			cmdLower := strings.ToLower(execCommand)

			for _, safe := range safeCommands {
				if strings.HasPrefix(cmdLower, safe) {
					if len(cmdLower) == len(safe) || cmdLower[len(safe)] == ' ' || cmdLower[len(safe)] == '-' {
						isSafeReadOnly = true
						break
					}
				}
			}

			sessionID := GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for executing shell command")
			}
			if !isSafeReadOnly {
				p, err := permissions.Request(ctx,
					permission.CreatePermissionRequest{
						SessionID:   sessionID,
						Path:        execWorkingDir,
						ToolCallID:  call.ID,
						ToolName:    BashToolName,
						Action:      "execute",
						Description: fmt.Sprintf("Execute command: %s", originalCommand),
						Params:      BashPermissionsParams(params),
					},
				)
				if err != nil {
					return fantasy.ToolResponse{}, err
				}
				if !p {
					return NewPermissionDeniedResponse(), nil
				}
			}

			// If explicitly requested as background, start immediately with detached context
			if params.RunInBackground {
				startTime := time.Now()
				bgManager := shell.GetBackgroundShellManager()
				bgManager.Cleanup()
				// Use background context so it continues after tool returns
				bgShell, err := bgManager.Start(context.Background(), execWorkingDir, blockFuncs(), execCommand, params.Description)
				if err != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("error starting background shell: %w", err)
				}

				// Wait a short time to detect fast failures (blocked commands, syntax errors, etc.)
				time.Sleep(1 * time.Second)
				stdout, stderr, done, execErr := bgShell.GetOutput()

				if done {
					// Command failed or completed very quickly
					bgManager.Remove(bgShell.ID)

					interrupted := shell.IsInterrupt(execErr)
					exitCode := shell.ExitCode(execErr)
					if exitCode == 0 && !interrupted && execErr != nil {
						return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
					}

					if interrupted || exitCode != 0 {
						return buildBashFailureResponse(startTime, params.Description, originalCommand, execCommand, bgShell.Shell.GetWorkingDir(), stdout, stderr, execErr, params.RunInBackground), nil
					}
					return buildBashSuccessResponse(startTime, params.Description, originalCommand, execCommand, bgShell.Shell.GetWorkingDir(), stdout, stderr, params.RunInBackground), nil
				}

				// Still running after fast-failure check - return as background job
				metadata := BashResponseMetadata{
					StartTime:        startTime.UnixMilli(),
					EndTime:          time.Now().UnixMilli(),
					Command:          originalCommand,
					ExecutedCommand:  execCommand,
					Description:      params.Description,
					WorkingDirectory: bgShell.WorkingDir,
					Background:       true,
					ShellID:          bgShell.ID,
				}
				response := fmt.Sprintf("Background shell started with ID: %s\n\nUse job_output tool to view output or job_kill to terminate.", bgShell.ID)
				return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
			}

			// Start synchronous execution with auto-background support
			startTime := time.Now()

			// Start with detached context so it can survive if moved to background
			bgManager := shell.GetBackgroundShellManager()
			bgManager.Cleanup()
			bgShell, err := bgManager.Start(context.Background(), execWorkingDir, blockFuncs(), execCommand, params.Description)
			if err != nil {
				return fantasy.ToolResponse{}, fmt.Errorf("error starting shell: %w", err)
			}

			// Wait for either completion, auto-background threshold, or context cancellation
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()

			autoBackgroundAfter := cmp.Or(params.AutoBackgroundAfter, DefaultAutoBackgroundAfter)
			autoBackgroundThreshold := time.Duration(autoBackgroundAfter) * time.Second
			timeout := time.After(autoBackgroundThreshold)

			var stdout, stderr string
			var done bool
			var execErr error

		waitLoop:
			for {
				select {
				case <-ticker.C:
					stdout, stderr, done, execErr = bgShell.GetOutput()
					if done {
						break waitLoop
					}
				case <-timeout:
					stdout, stderr, done, execErr = bgShell.GetOutput()
					break waitLoop
				case <-ctx.Done():
					// Incoming context was cancelled before we moved to background
					// Kill the shell and return error
					bgManager.Kill(bgShell.ID)
					return fantasy.ToolResponse{}, ctx.Err()
				}
			}

			if done {
				// Command completed within threshold - return synchronously
				// Remove from background manager since we're returning directly
				// Don't call Kill() as it cancels the context and corrupts the exit code
				bgManager.Remove(bgShell.ID)

				interrupted := shell.IsInterrupt(execErr)
				exitCode := shell.ExitCode(execErr)
				if exitCode == 0 && !interrupted && execErr != nil {
					return fantasy.ToolResponse{}, fmt.Errorf("[Job %s] error executing command: %w", bgShell.ID, execErr)
				}

				if interrupted || exitCode != 0 {
					return buildBashFailureResponse(startTime, params.Description, originalCommand, execCommand, bgShell.Shell.GetWorkingDir(), stdout, stderr, execErr, params.RunInBackground), nil
				}
				return buildBashSuccessResponse(startTime, params.Description, originalCommand, execCommand, bgShell.Shell.GetWorkingDir(), stdout, stderr, params.RunInBackground), nil
			}

			// Still running - keep as background job
			metadata := BashResponseMetadata{
				StartTime:        startTime.UnixMilli(),
				EndTime:          time.Now().UnixMilli(),
				Command:          originalCommand,
				ExecutedCommand:  execCommand,
				Description:      params.Description,
				WorkingDirectory: bgShell.WorkingDir,
				Background:       true,
				ShellID:          bgShell.ID,
			}
			response := fmt.Sprintf("Command is taking longer than expected and has been moved to background.\n\nBackground shell ID: %s\n\nUse job_output tool to view output or job_kill to terminate.", bgShell.ID)
			return fantasy.WithResponseMetadata(fantasy.NewTextResponse(response), metadata), nil
		}), []string{"execute"}, []string{"bash_commands"})
}

// formatOutput formats the output of a completed command with error handling
func formatOutput(stdout, stderr string, execErr error) string {
	interrupted := shell.IsInterrupt(execErr)
	exitCode := shell.ExitCode(execErr)

	stdout = truncateOutput(stdout)
	stderr = truncateOutput(stderr)

	errorMessage := stderr
	if errorMessage == "" && execErr != nil {
		errorMessage = execErr.Error()
	}

	if interrupted {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += "Command was aborted before completion"
	} else if exitCode != 0 {
		if errorMessage != "" {
			errorMessage += "\n"
		}
		errorMessage += fmt.Sprintf("Exit code %d", exitCode)
	}

	hasBothOutputs := stdout != "" && stderr != ""

	if hasBothOutputs {
		stdout += "\n"
	}

	if errorMessage != "" {
		stdout += "\n" + errorMessage
	}

	return stdout
}

func truncateOutput(content string) string {
	if len(content) <= MaxOutputLength {
		return content
	}

	halfLength := MaxOutputLength / 2
	start := content[:halfLength]
	end := content[len(content)-halfLength:]

	truncatedLinesCount := countLines(content[halfLength : len(content)-halfLength])
	return fmt.Sprintf("%s\n\n... [%d lines truncated] ...\n\n%s", start, truncatedLinesCount, end)
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func normalizeWorkingDir(path string) string {
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(path, fsext.WindowsWorkingDirDrive(), "")
	}
	return filepath.ToSlash(path)
}

func buildBashSuccessResponse(
	startTime time.Time,
	description string,
	originalCommand string,
	executedCommand string,
	workingDir string,
	stdout string,
	stderr string,
	background bool,
) fantasy.ToolResponse {
	truncatedStdout := truncateOutput(stdout)
	truncatedStderr := truncateOutput(stderr)
	output := formatOutput(stdout, stderr, nil)
	metadata := BashResponseMetadata{
		StartTime:        startTime.UnixMilli(),
		EndTime:          time.Now().UnixMilli(),
		Command:          originalCommand,
		ExecutedCommand:  executedCommand,
		Output:           output,
		Stdout:           truncatedStdout,
		Stderr:           truncatedStderr,
		Description:      description,
		WorkingDirectory: workingDir,
		Background:       background,
	}
	if output == "" {
		return fantasy.WithResponseMetadata(fantasy.NewTextResponse(BashNoOutput), metadata)
	}
	output += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(workingDir))
	return fantasy.WithResponseMetadata(fantasy.NewTextResponse(output), metadata)
}

func buildBashFailureResponse(
	startTime time.Time,
	description string,
	originalCommand string,
	executedCommand string,
	workingDir string,
	stdout string,
	stderr string,
	execErr error,
	background bool,
) fantasy.ToolResponse {
	output := formatOutput(stdout, stderr, execErr)
	exitCode := shell.ExitCode(execErr)
	retryable, failureKind := classifyBashFailure(originalCommand, workingDir, stdout, stderr, execErr)
	if output == "" {
		output = fmt.Sprintf("Command failed with exit code %d", exitCode)
	}
	output += fmt.Sprintf("\n\n<cwd>%s</cwd>", normalizeWorkingDir(workingDir))
	metadata := BashResponseMetadata{
		StartTime:        startTime.UnixMilli(),
		EndTime:          time.Now().UnixMilli(),
		Command:          originalCommand,
		ExecutedCommand:  executedCommand,
		Output:           output,
		Stdout:           truncateOutput(stdout),
		Stderr:           truncateOutput(stderr),
		Description:      description,
		WorkingDirectory: workingDir,
		ExitCode:         exitCode,
		Retryable:        retryable,
		FailureKind:      failureKind,
		Background:       background,
	}
	return fantasy.WithResponseMetadata(fantasy.NewTextErrorResponse(output), metadata)
}

func normalizeBashInvocation(command string, execWorkingDir string) (string, string) {
	if runtime.GOOS != "windows" {
		return command, execWorkingDir
	}
	if path, rest, ok := splitLeadingWindowsCD(command); ok {
		return quoteLeadingWindowsExecutable(rest), normalizeWindowsPath(path)
	}
	return quoteLeadingWindowsExecutable(command), normalizeWindowsPath(execWorkingDir)
}

func splitLeadingWindowsCD(command string) (string, string, bool) {
	before, after, ok := strings.Cut(command, "&&")
	if !ok {
		return "", "", false
	}

	prefix := strings.TrimSpace(before)
	if prefix == "" {
		return "", "", false
	}
	lowerPrefix := strings.ToLower(prefix)
	switch {
	case strings.HasPrefix(lowerPrefix, "cd /d "):
		prefix = strings.TrimSpace(prefix[len("cd /d "):])
	case strings.HasPrefix(lowerPrefix, "cd "):
		prefix = strings.TrimSpace(prefix[len("cd "):])
	default:
		return "", "", false
	}

	path := strings.Trim(prefix, `"'`)
	rest := strings.TrimSpace(after)
	if rest == "" || !isLikelyWindowsPath(path) {
		return "", "", false
	}
	return path, rest, true
}

func isLikelyWindowsPath(path string) bool {
	if len(path) < 3 {
		return false
	}
	drive := path[0]
	return ((drive >= 'a' && drive <= 'z') || (drive >= 'A' && drive <= 'Z')) &&
		path[1] == ':' &&
		(path[2] == '\\' || path[2] == '/')
}

func normalizeWindowsPath(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	return filepath.Clean(strings.ReplaceAll(path, "/", `\`))
}

func quoteLeadingWindowsExecutable(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return command
	}
	if strings.HasPrefix(command, `"`) || strings.HasPrefix(command, `'`) {
		return command
	}
	if !isLikelyWindowsPath(command) {
		return command
	}

	lower := strings.ToLower(command)
	bestEnd := -1
	for _, ext := range []string{".exe", ".bat", ".cmd", ".com", ".ps1", ".py"} {
		idx := strings.Index(lower, ext)
		if idx < 0 {
			continue
		}
		end := idx + len(ext)
		if end < len(command) {
			next := command[end]
			if next != ' ' && next != '\t' && next != '&' && next != '|' && next != ';' {
				continue
			}
		}
		if bestEnd == -1 || end < bestEnd {
			bestEnd = end
		}
	}
	if bestEnd <= 0 {
		return command
	}

	pathPart := command[:bestEnd]
	rest := strings.TrimLeft(command[bestEnd:], " \t")
	normalizedPath := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(pathPart, "/", `\`)))
	if rest == "" {
		return `"` + normalizedPath + `"`
	}
	return `"` + normalizedPath + `" ` + rest
}

func classifyBashFailure(command string, workingDir string, stdout string, stderr string, execErr error) (bool, string) {
	if runtime.GOOS != "windows" {
		return false, ""
	}
	if looksLikeWindowsShellMismatch(command, stdout, stderr, execErr) {
		return true, BashFailureKindWindowsShellMismatch
	}
	if !looksLikeWindowsPathCommand(command) {
		return false, ""
	}

	errorText := strings.ToLower(strings.Join([]string{
		stderr,
		stdout,
		errorString(execErr),
	}, "\n"))
	if strings.Contains(errorText, "no such file or directory") ||
		strings.Contains(errorText, "not a directory") ||
		strings.Contains(errorText, "could not parse command") ||
		strings.Contains(errorText, "invalid char escape") ||
		strings.Contains(errorText, `\p`) ||
		strings.Contains(errorText, `\u`) ||
		(strings.Contains(strings.ToLower(command), "cd ") && strings.Contains(errorText, "not found")) ||
		(isLikelyWindowsPath(strings.Trim(command, `"'`)) && strings.Contains(errorText, "not found")) {
		return true, BashFailureKindWindowsPathTranslation
	}
	return false, ""
}

func looksLikeWindowsShellMismatch(command string, stdout string, stderr string, execErr error) bool {
	errorText := strings.ToLower(strings.Join([]string{
		stderr,
		stdout,
		errorString(execErr),
	}, "\n"))
	commandText := strings.ToLower(strings.TrimSpace(command))
	if !strings.Contains(errorText, "executable file not found in $path") &&
		!strings.Contains(errorText, "not found") {
		return false
	}
	if strings.Contains(commandText, "| head") || strings.Contains(commandText, "| tail") {
		return true
	}
	if strings.Contains(commandText, "select-object") ||
		strings.Contains(commandText, "format-table") ||
		strings.Contains(commandText, "where-object") {
		return true
	}
	return false
}

func looksLikeWindowsPathCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if isLikelyWindowsPath(command) {
		return true
	}
	lowerCommand := strings.ToLower(command)
	if idx := strings.Index(lowerCommand, "cd "); idx >= 0 {
		segment := command[idx+3:]
		segment = strings.TrimLeft(segment, " ")
		segment = strings.TrimLeft(segment, "/d ")
		segment = strings.Trim(segment, `"'`)
		return isLikelyWindowsPath(segment)
	}
	return strings.Contains(command, `:\`) || strings.Contains(command, `:/`)
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
