package core

// precompact.go — o hook PreCompact. Um domínio: "antes de o Claude Code compactar a
// conversa, deixar a memória em dia e destravar o recall por turno".
//
// Contrato verificado em https://code.claude.com/docs/en/hooks (2026-11-22):
//   - stdin traz trigger ("manual" | "auto") e custom_instructions, mais os campos
//     comuns (session_id, transcript_path, cwd);
//   - PreCompact NÃO tem additionalContext: com exit 0, o STDOUT vira instrução
//     para o resumo da compactação; exit 2 ou decision "block" BLOQUEIA a compactação.
//
// Por isso este hook não reinjeta nada. A reinjeção já existe: o SessionStart com
// matcher "compact" (hooks.json) roda `recall` depois da compactação e devolve o
// bloco <anhur-memory> inteiro ao contexto novo.

import (
	"context"
	"encoding/json"
	"io"

	"github.com/Yoven/AnhurDB-SDK/v2/golang/v4/client"
)

// preCompactInput is the PreCompact stdin payload: the persist fields plus the trigger.
// hookInput is embedded so encoding/json promotes session_id/transcript_path/cwd.
type preCompactInput struct {
	hookInput
	Trigger string `json:"trigger"`
}

// cmdPreCompact resets the per-turn dedupe for the session, then persists the
// transcript delta exactly like the Stop hook does.
//
// Junior Tip [stdout stays EMPTY here]: on PreCompact, Claude Code feeds stdout to the
// summariser as compaction instructions. A stray log line on stdout would silently
// steer how the user's conversation is summarised. Everything this path says goes to
// plugin.log via logLine; nothing in persistHookInput prints to stdout.
//
// Junior Tip [why persist here when Stop already does]: compaction can fire in the
// MIDDLE of a long agentic turn (auto-compact at the context window), before that
// turn's Stop. Persisting first puts the turn-so-far in AnhurDB before the context
// that produced it is summarised away. The cursor makes it idempotent: the later
// Stop only sends lines added after this run.
//
// Junior Tip [reset BEFORE persist]: the reset is local and instant; persist talks to
// the network and can run into the hook timeout. Doing the cheap, certain step first
// means a slow AnhurDB cannot leave the dedupe hiding records the model just lost.
func cmdPreCompact(parentContext context.Context, cfg config, memory *client.Memory, hookStdin io.Reader) {
	var input preCompactInput
	if decodeError := json.NewDecoder(hookStdin).Decode(&input); decodeError != nil {
		logLine(cfg, "precompact: unreadable hook input: "+decodeError.Error())
	}
	if resetError := resetTurnSeenSet(cfg, input.SessionID); resetError != nil {
		logLine(cfg, "precompact: per-turn dedupe not reset (records stay hidden until it is): "+resetError.Error())
	}
	logLine(cfg, "precompact: trigger="+input.Trigger+" session="+input.SessionID+" — persisting before compaction")
	persistHookInput(parentContext, cfg, memory, input.hookInput)
}
