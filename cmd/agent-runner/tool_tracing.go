package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// extractSkillTarget parses the "target" field from an execute_command args
// JSON blob. Returns "command-executor" when the field is absent or blank so
// callers always get a non-empty label.
func extractSkillTarget(argsJSON string) string {
	var args struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err == nil && args.Target != "" {
		return args.Target
	}
	return "command-executor"
}

func executeToolCallWithTelemetry(ctx context.Context, name, argsJSON, callID string) string {
	start := time.Now()
	toolCtx, toolSpan := obs.startToolSpan(ctx,
		attribute.String("gen_ai.tool.name", name),
		attribute.String("gen_ai.tool.call.id", callID),
	)

	var result string
	if name == ToolExecuteCommand {
		skillName := extractSkillTarget(argsJSON)
		skillStart := time.Now()
		skillCtx, skillSpan := obs.startSkillSpan(toolCtx,
			attribute.String("skill.name", skillName),
			attribute.String("executor", "command-executor"),
			attribute.String("sidecar.container", "tool-executor"),
			attribute.String("rbac.scope", "namespace"),
		)
		result = executeToolCall(skillCtx, name, argsJSON)
		if strings.HasPrefix(result, "Error:") {
			err := fmt.Errorf("%s", result)
			markSpanError(skillSpan, err)
		} else {
			skillSpan.SetStatus(codes.Ok, "")
		}
		skillSpan.SetAttributes(attribute.Int64("duration_ms", time.Since(skillStart).Milliseconds()))
		skillSpan.End()
		obs.recordSkillDuration(toolCtx, skillName, time.Since(skillStart))
	} else {
		result = executeToolCall(toolCtx, name, argsJSON)
	}

	if strings.HasPrefix(result, "Error:") {
		err := fmt.Errorf("%s", result)
		markSpanError(toolSpan, err)
		obs.recordToolInvocation(toolCtx, name, "error")
	} else {
		toolSpan.SetStatus(codes.Ok, "")
		obs.recordToolInvocation(toolCtx, name, "success")
	}
	toolSpan.SetAttributes(attribute.Int64("duration_ms", time.Since(start).Milliseconds()))
	toolSpan.End()
	return result
}
