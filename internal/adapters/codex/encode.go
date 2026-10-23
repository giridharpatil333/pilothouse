package codex

import (
	"github.com/xasdta/pilothouse/internal/adapters"
	"github.com/xasdta/pilothouse/internal/adapters/hookkit"
	"github.com/xasdta/pilothouse/internal/api"
)

// EncodeResponse answers Codex within what its output parser accepts
// (codex-rs/hooks/src/engine/output_parser.rs):
//
//   - pre_tool: only deny (permissionDecision "deny" + a non-empty reason).
//     Codex marks allow-without-updatedInput and ask as "unsupported" and
//     reports the hook as failed, so those outcomes print nothing and
//     Codex's own approval policy applies.
//   - permission: decision.behavior allow | deny (+ message on deny). No
//     updatedInput/updatedPermissions/interrupt: Codex fails closed on them.
//   - session_start, prompt_submit: additionalContext; prompt_submit deny
//     uses decision:block.
//   - everything else: nothing.
func (Adapter) EncodeResponse(ev adapters.HookEvent, out adapters.HookOutcome) adapters.HookResponse {
	switch ev.Kind {
	case adapters.HookPreTool:
		if out.Behavior == api.BehaviorDeny {
			return hookkit.PreToolResponse(api.BehaviorDeny, out.Message, "")
		}
		return hookkit.PreToolResponse("", "", out.Context)
	case adapters.HookPermission:
		if out.Behavior == api.BehaviorAllow || out.Behavior == api.BehaviorDeny {
			return hookkit.PermissionResponse(out.Behavior, out.Message)
		}
	case adapters.HookPromptSubmit:
		if out.Behavior == api.BehaviorDeny {
			return hookkit.BlockResponse(out.Message)
		}
		return hookkit.ContextResponse("UserPromptSubmit", out.Context)
	case adapters.HookSessionStart:
		return hookkit.ContextResponse("SessionStart", out.Context)
	}
	return hookkit.NoOpinion
}

// FailResponse: no opinion, except a deny at pre_tool when
// approvals.fail_closed is on (deny is the one PreToolUse answer Codex
// honors).
func (Adapter) FailResponse(kind adapters.HookKind, failClosed bool) adapters.HookResponse {
	return hookkit.FailResponse(kind, failClosed)
}
