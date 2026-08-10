package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type agentRunRequest struct {
	Engine      string `json:"engine"`
	Cwd         string `json:"cwd"`
	Prompt      string `json:"prompt"`
	Timeout     int    `json:"timeout"`
	SessionID   string `json:"session_id"`
	WriteAccess bool   `json:"write_access"`
}

type agentRunResponse struct {
	OK           bool    `json:"ok"`
	Output       string  `json:"output"`
	Duration     float64 `json:"duration"`
	ArtifactsDir string  `json:"artifacts_dir"`
	SessionID    string  `json:"session_id,omitempty"`
}

// runAgentCommand 是给 LangGraph sidecar 使用的稳定 JSON 桥；stdout 只输出一份 JSON。
func runAgentCommand() int {
	var req agentRunRequest
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintln(os.Stderr, "invalid agent-run request:", err)
		return 2
	}
	req.Engine = normalizeAgent(req.Engine)
	if _, ok := AgentCmds[req.Engine]; !ok {
		fmt.Fprintln(os.Stderr, "unknown agent:", req.Engine)
		return 2
	}
	if strings.TrimSpace(req.Cwd) == "" || strings.TrimSpace(req.Prompt) == "" {
		fmt.Fprintln(os.Stderr, "cwd and prompt are required")
		return 2
	}
	if req.Timeout <= 0 {
		req.Timeout = cfg.TimeoutCode
	}
	res := runAgentInSessionWithAccess(
		req.Engine, req.Prompt, req.Cwd, req.Timeout, req.SessionID, req.WriteAccess, nil, nil,
	)
	out := agentRunResponse{
		OK: res.OK, Output: res.Output, Duration: res.Duration,
		ArtifactsDir: res.ArtifactsDir, SessionID: res.SessionID,
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "encode agent-run response:", err)
		return 1
	}
	return 0
}
